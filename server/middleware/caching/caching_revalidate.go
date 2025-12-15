package caching

import (
	"context"
	"net/http"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var _ Processor = (*RevalidateProcessor)(nil)

type rawRangeKey struct{}

type RefreshOption func(r *RevalidateProcessor)

type RevalidateProcessor struct{}

func (r *RevalidateProcessor) Lookup(c *Caching, req *http.Request) (bool, error) {
	if c.md == nil {
		return false, nil
	}
	// check if metadata is expired.
	if !hasExpired(c.md) {
		return true, nil
	}

	if c.md.HasComplete() && hasConditionHeader(c.md.Headers) {
		c.revalidate = true
		c.cacheStatus = storagev1.CacheRevalidateHit
		return false, nil
	}

	c.revalidate = true
	c.cacheStatus = storagev1.CacheRevalidateMiss

	// drop metadata
	if discardErr := c.bucket.DiscardWithMessage(req.Context(), c.id, "revalidate cache with expired"); discardErr != nil {
		c.log.Errorf("cache revalidate storage error when discarding of object's data: %s, err: %s",
			c.id.Key(), discardErr)
	}

	return false, nil
}

func (r *RevalidateProcessor) PreRequest(c *Caching, req *http.Request) (*http.Request, error) {
	if c.revalidate {
		// If headers check
		conditionHeader := false
		// ETag check, set If-None-Match
		if c.md.Headers.Get("ETag") != "" {
			req.Header.Set("If-None-Match", c.md.Headers.Get("ETag"))
			conditionHeader = true
		}
		// Last-Modified check, set If-Modified-Since
		if c.md.Headers.Get("Last-Modified") != "" {
			req.Header.Set("If-Modified-Since", c.md.Headers.Get("Last-Modified"))
			conditionHeader = true
		}
		// If status code is not 2xx , skip condition header
		if c.md.Code >= http.StatusMultipleChoices {
			conditionHeader = true
		}

		if !conditionHeader {
			c.log.Warnf("refresh error while get 'Etag' & 'Last-Modified' is nil, delete cache do proxy")
			_ = c.bucket.DiscardWithMessage(req.Context(), c.id, "refresh cache no condition header")
			return req, nil
		}

		rawRange := req.Header.Get("Range")
		if rawRange != "" {
			req = req.WithContext(context.WithValue(req.Context(), rawRangeKey{}, rawRange))
		}
		return req, nil
	}
	return req, nil
}

func (r *RevalidateProcessor) PostRequest(c *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	if c.revalidate {
		c.revalidate = false
		return r.revalidate(c, resp, req)
	}
	return resp, nil
}

func (r *RevalidateProcessor) revalidate(c *Caching, resp *http.Response, req *http.Request) (*http.Response, error) {
	if resp.StatusCode != http.StatusNotModified {
		c.cacheStatus = storagev1.CacheRevalidateMiss
		c.setXCache(resp)
		_ = c.bucket.DiscardWithMessage(req.Context(), c.id, "revalidate cache not StatusNotModified")
		return resp, nil
	}

	// update Last-Modified

	// TODO: lazilyRespond

	return resp, nil
}

func NewRevalidateProcessor(opts ...RefreshOption) Processor {
	return &RevalidateProcessor{}
}

// hasExpired checks if the metadata has expired based on the ExpiresAt timestamp.
// It returns true if the current time is after the ExpiresAt time, indicating that the metadata has expired.
func hasExpired(md *object.Metadata) bool {
	return time.Unix(md.ExpiresAt, 0).Before(time.Now())
}

// hasConditionHeader checks if the HTTP header contains either an ETag or Last-Modified field.
// It returns true if either of these fields is present, indicating that the header has a condition.
func hasConditionHeader(header http.Header) bool {
	return header.Get("ETag") != "" || header.Get("Last-Modified") != ""
}
