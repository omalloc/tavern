package caching

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omalloc/proxy/selector"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
	"github.com/omalloc/tavern/storage/representation"
)

func hasClientConditions(req *http.Request) bool {
	for name := range req.Header {
		for _, condition := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range"} {
			if strings.EqualFold(name, condition) {
				return true
			}
		}
	}
	return false
}

// net/http stores Host separately from Header, but it is still a legal Vary
// selection field. Capture the value actually written on the origin request.
func representationSelectionHeader(req *http.Request) http.Header {
	header := req.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	header.Set("Host", host)
	return header
}

func prepareRepresentationRequest(req *http.Request) *http.Request {
	upstream := req.Clone(req.Context())
	upstream.RequestURI = ""
	if upstream.URL.Host == "" {
		upstream.URL.Host = req.Host
	}
	if upstream.URL.Scheme == "" {
		upstream.URL.Scheme = xhttp.Scheme(req)
	}
	xhttp.RemoveHopByHopHeaders(upstream.Header)
	if address := req.Header.Get(protocol.InternalUpstreamAddr); address != "" {
		upstream = upstream.WithContext(selector.NewPeerContext(req.Context(), selector.NewPeer(selector.NewNode("http", address, nil))))
	}
	return upstream
}

func (c *Caching) representationKey(req *http.Request) (object.ResourceKey, error) {
	u := *prepareRepresentationRequest(req).URL
	// URL.Host may identify a dial target while Request.Host carries the
	// public authority. Virtual hosts are distinct resources even without Vary.
	if req.Host != "" {
		u.Host = req.Host
	}
	if !c.opt.IncludeQueryInCacheKey {
		u.RawQuery, u.ForceQuery = "", false
	}
	scope := c.opt.representation.isolationScope
	if peer := req.Header.Get(protocol.InternalUpstreamAddr); peer != "" {
		scope += "\x00peer\x00" + peer
	}
	return object.NewResourceKey(u.String(), c.opt.representation.namespace, scope)
}

func (c *Caching) bypassRepresentation(req *http.Request) {
	c.md, c.variantRef = nil, nil
	c.hit, c.cacheable, c.bypass = false, false, true
	c.cacheStatus = storage.BYPASS
	c.id, _ = newObjectIDFromRequest(req, "", c.opt.IncludeQueryInCacheKey)
}

func (c *Caching) proxyRepresentation(req *http.Request) (*http.Response, error) {
	repo, repoErr := c.resolveRepresentationRepository(req)
	prior, priorRef := c.md.Clone(), c.variantRef
	priorBucket := c.bucket
	c.bypassRepresentation(req)
	upstream := prepareRepresentationRequest(req)
	// Client conditions retain their own semantics. HEAD and body-carrying
	// requests never create body descriptors through this path.
	if requestBypassesCache(req) || hasClientConditions(req) || req.Method != http.MethodGet ||
		(req.Body != nil && req.Body != http.NoBody) || repoErr != nil {
		return c.executeUpstream(upstream)
	}
	state, bucket, err := c.beginRepresentationFill(req)
	if err != nil {
		return c.executeUpstream(upstream)
	}
	root := state.Resource()
	c.resourceMD = root
	c.bucket = bucket
	// A missing range in an existing generation has no reusable version
	// proof. Rebuild a full object under a fresh ID, then crop only the client
	// response. We never resume writes into a published partial generation.
	rebuildRange := prior != nil && upstream.Header.Get("Range") != "" && !strings.Contains(upstream.Header.Get("Range"), ",")
	if rebuildRange {
		upstream.Header.Del("Range")
	}
	validating := false
	if prior != nil && prior.HasComplete() && req.Header.Get("Range") == "" && priorRef != nil && prior.Representation != nil && prior.Representation.Incarnation == root.Resource.Incarnation && prior.Representation.PurgeEpoch == root.Resource.PurgeEpoch {
		if _, valid := opaqueETag(prior.Headers.Get("ETag")); valid {
			upstream.Header.Set("If-None-Match", prior.Headers.Get("ETag"))
			validating = true
		} else if _, err := http.ParseTime(prior.Headers.Get("Last-Modified")); err == nil {
			upstream.Header.Set("If-Modified-Since", prior.Headers.Get("Last-Modified"))
			validating = true
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		started := repo.Now()
		resp, err := c.executeUpstream(upstream)
		if err != nil {
			closeBody(resp)
			return nil, err
		}
		received := repo.Now()
		if resp.StatusCode == http.StatusNotModified {
			if validating && validValidation(prior, resp, upstream) {
				result, ok := c.validatedRepresentation(req, upstream, resp, prior, priorRef, state, bucket, priorBucket, started, received)
				if ok {
					return result, nil
				}
			}
			closeBody(resp)
			if attempt == 1 {
				return nil, fmt.Errorf("origin returned 304 without a reusable body")
			}
			upstream.Header.Del("If-None-Match")
			upstream.Header.Del("If-Modified-Since")
			validating, prior, priorRef = false, nil, nil
			continue
		}
		resp = c.admitRepresentation(req, upstream, resp, priorRef, state, bucket, started, received)
		if rebuildRange {
			return applyRepresentationRange(req, resp)
		}
		return resp, nil
	}
	return nil, errors.New("representation validation retry exhausted")
}

func (c *Caching) executeUpstream(req *http.Request) (*http.Response, error) {
	if c.origin == nil {
		return nil, errors.New("cache origin transport is not configured")
	}
	return c.origin.RoundTrip(req)
}

// Capture before network I/O and resolve the recorded bucket once. Neither
// admission nor body callbacks may select a replacement location on failure.
func (c *Caching) beginRepresentationFill(req *http.Request) (*representation.IndexState, storage.Bucket, error) {
	key, err := c.representationKey(req)
	if err != nil {
		return nil, nil, err
	}
	state, err := c.representationRepo.Capture(req.Context(), key)
	if err != nil {
		return nil, nil, err
	}
	location := state.Resource().Resource.ActiveLocation
	if c.bucket != nil && c.bucket.ID() == location {
		return state, c.bucket, nil
	}
	for _, candidate := range c.representationBuckets {
		if candidate.ID() == location {
			return state, candidate, nil
		}
	}
	return nil, nil, storage.ErrKeyNotFound
}

func (c *Caching) admitRepresentation(client, upstream *http.Request, resp *http.Response, previous *object.VariantRef, state *representation.IndexState, bucket storage.Bucket, started, received time.Time) *http.Response {
	headers, size, span, admitted := analyzeRepresentationResponse(upstream, resp, c.opt.SliceSize)
	if !admitted {
		return resp
	}
	fields, wildcard, err := varycontrol.Parse(headers.Values("Vary")...)
	if err != nil || wildcard {
		return resp
	}
	ref, err := representation.NewHTTPSelector(fields, representationSelectionHeader(upstream))
	if err != nil {
		return resp
	}
	expires, err := representationFreshness(headers, started, received)
	if err != nil {
		return resp
	}
	repo := c.representationRepo
	lease, err := repo.ReserveFor(client.Context(), state, ref, c.opt.VaryLimit)
	if err != nil {
		return resp
	}
	root := state.Resource().Resource
	generation := uuid.NewString()
	id, err := state.Key().RepresentationID(root.Incarnation, root.PurgeEpoch, ref.SelectorFingerprint, generation)
	if err != nil {
		repo.Release(lease)
		return resp
	}
	date, _ := http.ParseTime(headers.Get("Date"))
	ref.RepresentationID, ref.ResponseDate = id, date.Unix()
	md := &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, BlockSize: c.opt.SliceSize, Code: http.StatusOK,
		Size: size, RespUnix: received.Unix(), LastRefUnix: received.Unix(), ExpiresAt: expires, Headers: headers,
		Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion,
			ResourceID: root.ResourceID, Incarnation: root.Incarnation, PurgeEpoch: root.PurgeEpoch, ValidatedEpoch: root.RevalidateEpoch,
			BodyGeneration: generation, StorageSelectorFingerprint: ref.SelectorFingerprint, State: object.RepresentationFilling, Kind: object.RepresentationOrigin}}
	logger := c.log
	if err := repo.BindFill(client.Context(), state, lease, md); err != nil {
		repo.Release(lease)
		return resp
	}
	resp.Body = newRepresentationWriter(resp.Body, client.Context(), bucket, repo, state, lease, ref, previous, md, span, func(err error) { logger.Warnf("representation fill failed for %s: %v", id.HashStr(), err) })
	c.id, c.bypass, c.cacheStatus = id.Clone(), false, storage.CacheMiss
	if previous != nil {
		c.cacheStatus = storage.CacheRevalidateMiss
	}
	return resp
}

func validValidation(prior *object.Metadata, resp *http.Response, req *http.Request) bool {
	if prior == nil || !prior.HasComplete() {
		return false
	}
	// A 304 cannot relabel existing wire bytes with a different coding/type.
	for _, name := range []string{"Content-Encoding", "Content-Type"} {
		if len(resp.Header.Values(name)) > 1 {
			return false
		}
		if value := resp.Header.Get(name); value != "" && !strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(prior.Headers.Get(name))) {
			return false
		}
	}
	if length := resp.Header.Get("Content-Length"); length != "" {
		n, err := strconv.ParseUint(length, 10, 64)
		if err != nil || n != prior.Size {
			return false
		}
	}
	if value := req.Header.Get("If-None-Match"); value != "" {
		old, valid := opaqueETag(value)
		if !valid {
			return false
		}
		if current := resp.Header.Get("ETag"); current != "" {
			fresh, valid := opaqueETag(current)
			return valid && fresh == old
		}
		return true
	}
	if value := req.Header.Get("If-Modified-Since"); value != "" {
		if current := resp.Header.Get("Last-Modified"); current != "" {
			old, e1 := http.ParseTime(value)
			fresh, e2 := http.ParseTime(current)
			return e1 == nil && e2 == nil && old.Equal(fresh)
		}
		return true
	}
	return false
}

func (c *Caching) validatedRepresentation(client, upstream *http.Request, validation *http.Response, prior *object.Metadata, previous *object.VariantRef, state *representation.IndexState, bucket, readBucket storage.Bucket, started, received time.Time) (*http.Response, bool) {
	md := prior.Clone()
	updates := validation.Header.Clone()
	xhttp.RemoveHopByHopHeaders(updates)
	for name, values := range updates {
		if name == "Content-Length" || name == "Content-Range" {
			continue
		}
		md.Headers[name] = slices.Clone(values)
	}
	if validation.Header.Get("Age") == "" {
		age, _ := strconv.ParseInt(prior.Headers.Get("Age"), 10, 64)
		resident := max(int64(0), received.Unix()-prior.RespUnix)
		age = saturatedAge(max(int64(0), age), resident)
		md.Headers.Set("Age", strconv.FormatInt(age, 10))
	}
	md.Headers.Del("Content-Range")
	md.Headers.Set("Content-Length", strconv.FormatUint(md.Size, 10))
	md.RespUnix, md.LastRefUnix = received.Unix(), received.Unix()
	md.Representation.ValidatedEpoch = state.Resource().Resource.RevalidateEpoch
	if md.Representation.StorageSelectorFingerprint == "" {
		md.Representation.StorageSelectorFingerprint = previous.SelectorFingerprint
	}
	// Open the old body before confirming a 304. Missing files trigger one
	// unconditional origin request rather than an empty synthetic 200.
	readerContext := *c
	readerContext.id, readerContext.md, readerContext.bucket = md.ID, md, readBucket
	readerContext.resourceMD = state.Resource()
	response, err := readerContext.lazilyRespond(client, 0, int64(md.Size-1))
	if err != nil {
		closeBody(response)
		return nil, false
	}
	closeBody(validation)
	c.md, c.id, c.bucket = md, md.ID.Clone(), bucket
	c.cacheStatus, c.bypass = storage.BYPASS, true
	c.variantRef = nil
	fields, wildcard, parseErr := varycontrol.Parse(md.Headers.Values("Vary")...)
	expires, policyErr := representationFreshness(md.Headers, started, received)
	md.ExpiresAt = expires
	if parseErr == nil && !wildcard && policyErr == nil {
		ref, err := representation.NewHTTPSelector(fields, representationSelectionHeader(upstream))
		if err == nil {
			repo := c.representationRepo
			lease, err := repo.ReserveFor(client.Context(), state, ref, c.opt.VaryLimit)
			if err == nil {
				date, _ := http.ParseTime(md.Headers.Get("Date"))
				ref.RepresentationID, ref.ResponseDate = md.ID.Clone(), date.Unix()
				published, err := repo.Publish(client.Context(), state, lease, ref, md, previous, func() error { return bucket.Store(client.Context(), md) })
				if err != nil {
					c.log.Warnf("representation validation commit failed: %v", err)
				}
				if published {
					ref.Revision = md.Representation.Revision
					c.cacheStatus, c.bypass = storage.CacheRevalidateHit, false
					copy := ref.Clone()
					c.variantRef = &copy
				}
			}
		}
	}
	// Response assembly owns a header clone; use normalized age without
	// replacing the origin's selected Date or sharing mutable headers.
	response.Header = md.Headers.Clone()
	c.decorateRepresentation(response)
	return response, true
}
