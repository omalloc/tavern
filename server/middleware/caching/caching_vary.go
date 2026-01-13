package caching

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/kelindar/bitmap"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var (
	ErrHeaderNoMatchVaryKey  = errors.New("header no match vary key")
	ErrHeaderNoMatchVaryData = errors.New("header no match vary data")

	// ErrVarySizeLimited indicates the number of Vary versions has exceeded the maximum limit.
	ErrVarySizeLimited = errors.New("vary size exceed limit")

	// ErrVaryDowngradeNormal indicates the cache has been downgraded from Vary to normal cache.
	ErrVaryDowngradeNormal = errors.New("vary downgrade to normal cache")
)

// Compile-time interface implementation check.
var _ Processor = (*VaryProcessor)(nil)

type VaryOption func(r *VaryProcessor)

type VaryProcessor struct {
	maxLimit      int
	varyIgnoreKey map[string]struct{}
}

// Lookup checks if a cached response exists for the given request.
// It returns true if a matching Vary cache entry is found, false otherwise.
//
// The lookup process:
//  1. Check if the request has no-cache directive
//  2. Verify if the cached object has Vary index
//  3. Find the matching Vary cache based on request headers
func (v *VaryProcessor) Lookup(caching *Caching, req *http.Request) (bool, error) {
	if caching.hasNoCache() {
		return false, nil
	}

	// Check if the cached object has Vary index.
	if !caching.md.IsVary() {
		return true, nil
	}

	// Find the matching Vary cache.
	vmd := v.lookup(caching, req)
	if vmd == nil {
		// MISS: No matching Vary cache found for current request.
		return false, nil
	}

	// HIT: Found matching Vary cache, update caching context.
	caching.rootmd = caching.md
	caching.id = vmd.ID
	caching.md = vmd
	return true, nil
}

// PreRequest performs pre-processing before the request is forwarded to the origin.
// Currently, this is a no-op for VaryProcessor.
func (v *VaryProcessor) PreRequest(_ *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

// PostRequest processes the response from the origin server and handles Vary caching.
// It converts the response to a Vary-aware cache structure if the response contains
// Vary headers.
func (v *VaryProcessor) PostRequest(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	if caching.md.IsVaryCache() {
		return resp, nil
	}

	// Convert to Vary metadata and upgrade cache structure if needed.
	varyMetadata, err := v.convertVaryMetadata(caching, resp)
	if err != nil && !errors.Is(err, ErrHeaderNoMatchVaryKey) {
		caching.log.Errorf("PostRequest convertVaryMetadata failed: %s", err)
		return resp, nil
	}

	// No Vary header matched, return original response.
	if varyMetadata == nil {
		return resp, nil
	}

	// Build Vary index for retrieving all Vary versions of this URL.
	originVary := caching.md.Headers.Values("Vary")
	originVary = append(originVary, resp.Header.Values("Vary")...)

	caching.rootmd = caching.md
	caching.rootmd.ID = caching.id
	caching.rootmd.Size = 0
	caching.rootmd.Flags = object.FlagVaryIndex
	caching.rootmd.Chunks = bitmap.Bitmap{}
	caching.rootmd.Parts = bitmap.Bitmap{}
	caching.rootmd.Headers = http.Header{
		"Vary": varycontrol.Clean(originVary...),
	}

	// Inherit timestamps from root metadata.
	varyMetadata.RespUnix = caching.rootmd.RespUnix
	varyMetadata.LastRefUnix = caching.rootmd.LastRefUnix
	varyMetadata.ExpiresAt = caching.rootmd.ExpiresAt

	caching.md = varyMetadata
	caching.id = varyMetadata.ID

	return resp, nil
}

// lookup finds the matching Vary cache entry based on request headers.
func (v *VaryProcessor) lookup(caching *Caching, req *http.Request) *object.Metadata {
	varyKey := varycontrol.Clean(caching.md.Headers.Values("Vary")...)

	// Generate object ID based on Vary data from request headers.
	vid, err := newObjectIDFromRequest(req, varyKey.VaryData(req.Header), caching.opt.IncludeQueryInCacheKey)
	if err != nil {
		return nil
	}

	vmd, err := caching.bucket.Lookup(req.Context(), vid)
	if err != nil {
		return nil
	}

	return vmd
}

// convertVaryMetadata handles the conversion and creation of Vary-aware cache metadata.
// It processes both cases:
//   - When the origin response has no Vary header but cached metadata has Vary info
//   - When the origin response contains Vary header
func (v *VaryProcessor) convertVaryMetadata(caching *Caching, resp *http.Response) (*object.Metadata, error) {
	metaVary := varycontrol.Clean(caching.md.Headers.Values("Vary")...)
	respVary := varycontrol.Clean(resp.Header.Values("Vary")...)

	if caching.log.Enabled(log.LevelDebug) && (len(metaVary) > 0 || len(respVary) > 0) {
		caching.log.Debugf("convertVaryMetadata: metaVaryKey: %s, respVaryKey: %s", metaVary, respVary)
	}

	// Case 1: Origin response has no Vary header.
	if len(respVary) <= 0 {
		return v.handleNoResponseVary(caching, resp, metaVary)
	}

	// Case 2: Origin response has Vary header.
	return v.handleResponseVary(caching, resp, metaVary, respVary)
}

// handleNoResponseVary processes the case when origin response has no Vary header.
func (v *VaryProcessor) handleNoResponseVary(caching *Caching, resp *http.Response, metaVary varycontrol.Key) (*object.Metadata, error) {
	// No cached Vary info exists, skip Vary processing.
	if len(metaVary) <= 0 {
		return nil, ErrHeaderNoMatchVaryKey
	}

	// Generate Vary data from current request headers.
	varyData := metaVary.VaryData(caching.req.Header)
	if varyData == "" {
		// Request headers don't match Vary requirements, discard old cache.
		if err := caching.bucket.Discard(context.Background(), caching.id); err != nil {
			caching.log.Errorf("request header not match vary, discard old cache err: %v", err)
		}
		if caching.rootmd != nil {
			caching.rootmd = nil
		}
		return nil, ErrHeaderNoMatchVaryData
	}

	// Check Vary version limit.
	if len(caching.md.VirtualKey) >= v.maxLimit {
		caching.log.Errorf("vary version exceed limit: %d", len(caching.md.VirtualKey))
		return nil, ErrVarySizeLimited
	}

	// Append new Vary data if not already exists.
	if !slices.Contains(caching.md.VirtualKey, varyData) {
		caching.md.VirtualKey = append(caching.md.VirtualKey, varyData)
	} else {
		caching.log.Debugf("vary data already exist: %s", varyData)
	}

	l2MetaID, err := newObjectIDFromRequest(caching.req, varyData, caching.opt.IncludeQueryInCacheKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create hash-key: %w", err)
	}

	cl, _ := strconv.Atoi(resp.Header.Get("Content-Length"))
	return &object.Metadata{
		ID:        l2MetaID,
		RespUnix:  time.Now().Unix(),
		Code:      resp.StatusCode,
		Size:      uint64(cl),
		BlockSize: caching.md.BlockSize,
		Chunks:    bitmap.Bitmap{},
		Parts:     bitmap.Bitmap{},
		Headers:   resp.Header.Clone(),
		ExpiresAt: caching.md.ExpiresAt,
		Flags:     object.FlagVaryCache,
	}, nil
}

// handleResponseVary processes the case when origin response has Vary header.
func (v *VaryProcessor) handleResponseVary(caching *Caching, resp *http.Response, metaVary, respVary varycontrol.Key) (*object.Metadata, error) {
	var varyData string

	// Cached Vary key exists, compare with response Vary key.
	if len(metaVary) > 0 {
		if metaVary.Compare(respVary) {
			// Vary keys match, try to find existing Vary cache.
			varyData = metaVary.VaryData(caching.req.Header)
			varyKey, _ := newObjectIDFromRequest(caching.req, varyData, caching.opt.IncludeQueryInCacheKey)
			varyMeta, err := caching.bucket.Lookup(caching.req.Context(), varyKey)
			if err != nil {
				caching.log.Warnf("Vary key lookup failed: %v", err)
			}
			if varyMeta != nil {
				caching.log.Debugf("Vary header match, returning existing vary metadata")
				return varyMeta, nil
			}
		} else {
			// Vary keys differ, origin has updated Vary header, rebuild cache.
			caching.log.Infof("Vary header changed, rebuilding vary cache")
			if discardErr := caching.bucket.Discard(context.Background(), caching.id); discardErr != nil && !os.IsNotExist(discardErr) {
				caching.log.Errorf("error discarding old vary cache: %s", discardErr)
			}

			caching.md.VirtualKey = nil
			if len(respVary) <= 0 {
				// Origin removed Vary header, downgrade to normal cache.
				caching.md.Headers.Del("Vary")
				caching.md.Flags = object.FlagCache
				caching.log.Debugf("Vary header removed by origin, downgrading to normal cache")
				return nil, ErrVaryDowngradeNormal
			}

			varyData = respVary.VaryData(caching.req.Header)
		}

		// Build new Vary cache object.
		varyObjectID, _ := newObjectIDFromRequest(caching.req, varyData, caching.opt.IncludeQueryInCacheKey)
		return v.upgrade(caching, resp, varyObjectID, varyData)
	}

	// No metaVary exists, this is the first Vary request for this resource.
	if caching.md.Chunks.Count() > 0 {
		if discardErr := caching.bucket.DiscardWithMessage(context.Background(), caching.id, "upgrading cache to vary structure"); discardErr != nil {
			caching.log.Errorf("error discarding cache for vary upgrade: %s", discardErr)
		}
	}

	caching.md.VirtualKey = nil
	varyData = respVary.VaryData(caching.req.Header)
	varyObjectID, _ := newObjectIDFromRequest(caching.req, varyData, caching.opt.IncludeQueryInCacheKey)
	return v.upgrade(caching, resp, varyObjectID, varyData)
}

// upgrade converts a normal cache object to a Vary-aware cache structure.
// It creates a new Vary metadata entry and updates the cache flags.
func (v *VaryProcessor) upgrade(c *Caching, resp *http.Response, id *object.ID, varyData string) (*object.Metadata, error) {
	virtualKey := varycontrol.Clean(append(c.md.VirtualKey, varyData)...)

	if len(virtualKey) > v.maxLimit {
		c.log.Errorf("Vary version limit exceeded: %d", len(c.md.VirtualKey))
		return nil, ErrVarySizeLimited
	}

	c.md.VirtualKey = virtualKey
	c.md.ID = id
	c.md.Flags = object.FlagVaryIndex

	// Merge Vary headers from response and existing metadata.
	headers := c.md.Headers.Clone()
	newVaryKey := resp.Header.Values("Vary")
	newVaryKey = append(newVaryKey, headers.Values("Vary")...)
	headers.Del("Vary")
	for _, key := range varycontrol.Clean(newVaryKey...) {
		headers.Add("Vary", key)
	}

	// Parse content length from response.
	cr, err := xhttp.ParseContentRange(resp.Header)
	if err != nil {
		c.log.Debugf("ParseContentRange failed (may be chunked response): %v", err)
	}
	c.log.Infof("Vary upgrade completed, content-length: %d", cr.ObjSize)

	// Create new Vary metadata.
	now := time.Now().Unix()
	return &object.Metadata{
		ID:          id,
		RespUnix:    now,
		LastRefUnix: now,
		Code:        resp.StatusCode,
		Size:        cr.ObjSize,
		BlockSize:   c.md.BlockSize,
		Chunks:      bitmap.Bitmap{},
		Parts:       bitmap.Bitmap{},
		Headers:     headers,
		ExpiresAt:   c.md.ExpiresAt,
		Flags:       object.FlagVaryCache,
	}, nil
}

// NewVaryProcessor creates a new VaryProcessor with the given options.
// Default configuration:
//   - maxLimit: 100 (maximum Vary versions per URL)
func NewVaryProcessor(opts ...VaryOption) *VaryProcessor {
	v := &VaryProcessor{
		maxLimit:      100,
		varyIgnoreKey: make(map[string]struct{}),
	}

	for _, opt := range opts {
		opt(v)
	}
	return v
}

// WithVaryMaxLimit sets the maximum number of Vary versions allowed per URL.
func WithVaryMaxLimit(limit int) VaryOption {
	return func(r *VaryProcessor) {
		r.maxLimit = limit
	}
}

// WithVaryIgnoreKeys specifies header keys to be ignored during Vary processing.
func WithVaryIgnoreKeys(keys ...string) VaryOption {
	return func(r *VaryProcessor) {
		for _, key := range keys {
			r.varyIgnoreKey[key] = struct{}{}
		}
	}
}
