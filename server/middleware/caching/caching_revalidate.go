package caching

import (
	"context"
	"net/http"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
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

	if c.log.Enabled(log.LevelDebug) {
		c.log.Debugf("cache freshness ExpiresAt %s for object: %s",
			time.Unix(c.md.ExpiresAt, 0).Format(time.DateTime), c.id.Key())
	}

	if c.md.HasComplete() && hasConditionHeader(c.md.Headers) {
		c.revalidate = true
		c.cacheStatus = storagev1.CacheRevalidateHit
		return false, nil
	}

	// metadata is expired and no-fullyiable chunks file.
	// cannot revalidate, treat as cache miss
	c.revalidate = false
	c.cacheStatus = storagev1.CacheMiss

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

	// freshness metadata
	_ = r.freshness(c, resp)

	// lazilyRespond
	if raw := req.Context().Value(rawRangeKey{}); raw != nil {
		rawRange := raw.(string)
		rng, err := xhttp.SingleRange(rawRange, c.md.Size)
		if err != nil {
			return nil, xhttp.NewBizError(http.StatusRequestedRangeNotSatisfiable, nil)
		}
		c.log.Debugf("freshness cache by RawRange bytes=%d-%d", rng.Start, rng.End)
		return c.lazilyRespond(req, rng.Start, rng.End)
	}

	end := int64(0)
	if c.md.Size > 0 {
		end = int64(c.md.Size - 1)
	}

	c.log.Debugf("freshness cache by RawRange bytes=%d-%d", 0, end)
	return c.lazilyRespond(req, 0, end)
}

func (r *RevalidateProcessor) freshness(c *Caching, resp *http.Response) bool {
	expiredAt, cacheable := xhttp.ParseCacheTime("", resp.Header)
	if !cacheable {
		return false
	}

	now := time.Now()
	metadata := c.md.Clone()
	metadata.ExpiresAt = now.Add(expiredAt).Unix()
	metadata.RespUnix = now.Unix()
	metadata.LastRefUnix = now.Unix()

	cloneHeaders := []string{"Last-Modified", "ETag", "Cache-Control"}
	for _, name := range cloneHeaders {
		value := resp.Header.Get(name)
		if value != "" {
			metadata.Headers.Set(name, value)
		}
	}
	c.cacheable = true
	c.md = metadata

	// save freshness metadata
	_ = c.bucket.Store(c.req.Context(), c.md)
	return true
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
