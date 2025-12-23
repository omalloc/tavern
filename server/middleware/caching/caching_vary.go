package caching

import (
	"net/http"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var _ Processor = (*VaryProcessor)(nil)

type VaryOption func(r *VaryProcessor)

type VaryProcessor struct {
	maxLimit      int
	varyIgnoreKey map[string]struct{}
}

// Lookup implements Processor.
func (v *VaryProcessor) Lookup(caching *Caching, req *http.Request) (bool, error) {
	if caching.hasNoCache() {
		return false, nil
	}

	// check has Vary index
	if caching.md.IsVary() {
		// find vary index
		vmd := v.findVaryData(caching, req)
		if vmd == nil {
			// MISS current vary request.
			return false, nil
		}

		// HIT current vary request.
		caching.rootmd = caching.md

		caching.id = vmd.ID
		caching.md = vmd
		return true, nil
	}

	return true, nil
}

// PreRequest implements Processor.
func (v *VaryProcessor) PreRequest(caching *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

// PostRequest implements Processor.
func (v *VaryProcessor) PostRequest(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {

	if !caching.md.IsVaryCache() {
		// TODO: store vary metadata, vary index, upgrade current object to vary object.
	}

	return resp, nil
}

func (v *VaryProcessor) findVaryData(caching *Caching, req *http.Request) *object.Metadata {
	varyKey := varycontrol.Clean(caching.md.Headers.Values("Vary")...)

	// new object ID by vary data
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

func WithVaryMaxLimit(limit int) VaryOption {
	return func(r *VaryProcessor) {
		r.maxLimit = limit
	}
}

func WithVaryIgnoreKeys(keys ...string) VaryOption {
	return func(r *VaryProcessor) {
		for _, key := range keys {
			r.varyIgnoreKey[key] = struct{}{}
		}
	}
}
