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
	return object.NewResourceKey(u.String(), c.opt.representation.namespace, c.opt.representation.isolationScope)
}

func (c *Caching) bypassRepresentation(req *http.Request) {
	c.md, c.variantRef = nil, nil
	c.hit, c.cacheable, c.bypass = false, false, true
	c.cacheStatus = storage.BYPASS
	c.id, _ = newObjectIDFromRequest(req, "", c.opt.IncludeQueryInCacheKey)
}

func (c *Caching) proxyRepresentation(req *http.Request) (*http.Response, error) {
	config := c.opt.representation
	prior, priorRef := c.md.Clone(), c.variantRef
	c.bypassRepresentation(req)
	upstream := prepareRepresentationRequest(req)
	fetch := func(request *http.Request) (*http.Response, error) {
		return c.proxyClient.Do(request, false, c.opt.CollapsedRequestWaitTimeout.AsDuration())
	}
	// Client conditions retain their own 304 semantics. HEAD, Range and body-
	// carrying requests do not create complete descriptors through this path.
	if requestBypassesCache(req) || hasClientConditions(req) || req.Method != http.MethodGet || req.Header.Get("Range") != "" ||
		(req.Body != nil && req.Body != http.NoBody) || config.repository == nil {
		return fetch(upstream)
	}
	key, err := c.representationKey(req)
	if err != nil {
		return fetch(upstream)
	}
	repo := config.repository
	state, err := repo.Capture(req.Context(), key)
	if err != nil {
		return fetch(upstream)
	}
	root := state.Resource()
	c.resourceMD = root
	var bucket storage.Bucket
	// The lookup already resolved an existing selected location. For cold
	// roots use the fixed initial bucket, never a new dynamic selection.
	if c.bucket != nil && c.bucket.ID() == root.Resource.ActiveLocation {
		bucket = c.bucket
	}
	for _, candidate := range c.representationBuckets {
		if candidate.ID() == root.Resource.ActiveLocation {
			bucket = candidate
			break
		}
	}
	if bucket == nil {
		return fetch(upstream)
	}
	c.bucket = bucket
	validating := false
	if prior != nil && priorRef != nil && prior.Representation != nil && prior.Representation.Incarnation == root.Resource.Incarnation && prior.Representation.PurgeEpoch == root.Resource.PurgeEpoch {
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
		resp, err := fetch(upstream)
		if err != nil {
			closeBody(resp)
			return nil, err
		}
		received := repo.Now()
		if resp.StatusCode == http.StatusNotModified {
			if validating && validValidation(prior, resp, upstream) {
				result, ok := c.validatedRepresentation(req, upstream, resp, prior, priorRef, state, bucket, started, received)
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
		return c.admitRepresentation(req, upstream, resp, priorRef, state, bucket, started, received), nil
	}
	return nil, errors.New("representation validation retry exhausted")
}

func (c *Caching) admitRepresentation(client, upstream *http.Request, resp *http.Response, previous *object.VariantRef, state *representation.IndexState, bucket storage.Bucket, started, received time.Time) *http.Response {
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 || resp.Body == nil || resp.Header.Get("Content-Range") != "" || c.opt.SliceSize == 0 {
		return resp
	}
	headers := resp.Header.Clone()
	xhttp.RemoveHopByHopHeaders(headers)
	length, err := strconv.ParseInt(headers.Get("Content-Length"), 10, 64)
	if err != nil || length != resp.ContentLength || len(headers.Values("Content-Length")) != 1 {
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
	repo := c.opt.representation.repository
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
		Size: uint64(length), RespUnix: received.Unix(), LastRefUnix: received.Unix(), ExpiresAt: expires, Headers: headers,
		Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion,
			ResourceID: root.ResourceID, Incarnation: root.Incarnation, PurgeEpoch: root.PurgeEpoch, ValidatedEpoch: root.RevalidateEpoch,
			BodyGeneration: generation, StorageSelectorFingerprint: ref.SelectorFingerprint, State: object.RepresentationFilling, Kind: object.RepresentationOrigin}}
	resp.Body = newRepresentationWriter(resp.Body, client.Context(), bucket, repo, state, lease, ref, previous, md, func(err error) { c.log.Warnf("representation fill failed for %s: %v", id.HashStr(), err) })
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

func (c *Caching) validatedRepresentation(client, upstream *http.Request, validation *http.Response, prior *object.Metadata, previous *object.VariantRef, state *representation.IndexState, bucket storage.Bucket, started, received time.Time) (*http.Response, bool) {
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
	readerContext.id, readerContext.md, readerContext.bucket = md.ID, md, bucket
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
			repo := c.opt.representation.repository
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
