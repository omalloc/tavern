package caching

import (
	"net/http"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/server/middleware"
)

// Legacy descriptors have mutable physical IDs. Incremental range filling is
// permitted only after a strong If-Range proof. Without it, fetch one complete
// replacement and crop the client's response after the ordinary writer, just
// as the v2 path rebuilds under a new immutable generation.
func (c *Caching) fetchLegacyMissingRange(client *http.Request) (*http.Response, error) {
	if hasClientConditions(client) {
		c.bypass, c.cacheable, c.md, c.cacheStatus = true, false, nil, storage.BYPASS
		return c.executeUpstream(prepareRepresentationRequest(client))
	}
	old := c.md.Clone()
	tag := old.Headers.Get("ETag")
	_, valid := opaqueETag(tag)
	conditional := valid && !strings.HasPrefix(tag, "W/") && len(old.Headers.Values("ETag")) == 1
	wire := prepareRepresentationRequest(client)
	if conditional {
		wire.Header.Set("If-Range", tag)
	} else {
		wire.Header.Del("Range")
	}
	resp, err := c.executeUpstream(wire)
	if err != nil {
		closeBody(resp)
		return nil, err
	}
	proved := false
	if conditional && resp.StatusCode == http.StatusPartialContent {
		_, size, _, shape := analyzeRepresentationResponse(wire, resp, c.opt.SliceSize)
		proved = shape && size == old.Size && resp.Header.Get("ETag") == tag && len(resp.Header.Values("ETag")) == 1
		if !proved {
			closeBody(resp)
			wire.Header.Del("Range")
			wire.Header.Del("If-Range")
			resp, err = c.executeUpstream(wire)
			if err != nil {
				closeBody(resp)
				return nil, err
			}
		}
	}
	if !proved {
		if resp.StatusCode != http.StatusOK {
			c.bypass, c.cacheable, c.md, c.cacheStatus = true, false, nil, storage.BYPASS
			return resp, nil
		}
		if err = c.bucket.DiscardWithMessage(client.Context(), old.ID, "legacy range requires complete replacement without strong version proof"); err != nil {
			// A failed retirement must not write into an ambiguous old descriptor.
			c.bypass, c.cacheable, c.md, c.cacheStatus = true, false, nil, storage.BYPASS
			return applyRepresentationRange(client, resp)
		}
		c.md, c.cacheStatus = nil, storage.CacheMiss
	}
	// Run normal admission, metadata and fixed writer setup against the response
	// whose headers we have just verified. Range expansion must not run a second
	// time or resolve an open range against the old representation size.
	origin, processor := c.origin, c.processor
	chain := make(ProcessorChain, 0, len(*processor))
	for _, item := range *processor {
		if _, fill := item.(*fillRange); !fill {
			chain = append(chain, item)
		}
	}
	c.processor = &chain
	c.origin = middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) { return resp, nil })
	defer func() { c.origin, c.processor = origin, processor }()
	response, err := c.doProxy(wire, false)
	if err != nil {
		return response, err
	}
	if !proved {
		return applyRepresentationRange(client, response)
	}
	return response, nil
}
