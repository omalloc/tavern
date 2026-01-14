package caching

import (
	"context"
	"math/rand"
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

// calculateSoftTTL calculates the soft expiration time based on fuzzy_refresh_rate.
// soft_ttl = hard_ttl * fuzzy_ratio
// For example, if hard_ttl is 600s and fuzzy_ratio is 0.8, soft_ttl = 480s
func calculateSoftTTL(respUnix, expiresAt int64, fuzzyRate float64) int64 {
	hardTTL := expiresAt - respUnix
	if hardTTL <= 0 {
		return expiresAt
	}
	
	// Ensure fuzzy rate is between 0 and 1
	if fuzzyRate <= 0 || fuzzyRate >= 1 {
		fuzzyRate = 0.8 // default to 0.8 if invalid
	}
	
	softTTL := int64(float64(hardTTL) * fuzzyRate)
	return respUnix + softTTL
}

// shouldTriggerFuzzyRefresh determines if we should trigger an async refresh
// based on the current position in the [soft_ttl, hard_ttl) interval.
// The probability increases linearly as we approach hard_ttl.
func shouldTriggerFuzzyRefresh(now, softTTL, hardTTL int64) bool {
	if now < softTTL {
		// Before soft TTL, no refresh needed
		return false
	}
	
	if now >= hardTTL {
		// After hard TTL, force refresh (handled by hasExpired)
		return false
	}
	
	// In the fuzzy refresh zone [soft_ttl, hard_ttl)
	// Calculate linear probability: P = (now - soft_ttl) / (hard_ttl - soft_ttl)
	totalWindow := float64(hardTTL - softTTL)
	if totalWindow <= 0 {
		return false
	}
	
	elapsed := float64(now - softTTL)
	probability := elapsed / totalWindow
	
	// Random trigger based on probability
	return rand.Float64() < probability
}

func (r *RevalidateProcessor) Lookup(c *Caching, req *http.Request) (bool, error) {
	if c.md == nil {
		return false, nil
	}
	
	now := time.Now().Unix()
	hardTTL := c.md.ExpiresAt
	
	// Fuzzy Refresh Logic
	if c.opt.FuzzyRefresh && c.opt.FuzzyRefreshRate > 0 {
		softTTL := calculateSoftTTL(c.md.RespUnix, c.md.ExpiresAt, c.opt.FuzzyRefreshRate)
		
		// Check if we're in the fuzzy refresh zone [soft_ttl, hard_ttl)
		if now >= softTTL && now < hardTTL {
			// We're in the fuzzy refresh zone
			if shouldTriggerFuzzyRefresh(now, softTTL, hardTTL) {
				// Trigger async background refresh
				if c.md.HasComplete() && hasConditionHeader(c.md.Headers) {
					c.log.Debugf("fuzzy refresh triggered for object: %s (soft_ttl: %s, hard_ttl: %s)",
						c.id.Key(),
						time.Unix(softTTL, 0).Format(time.DateTime),
						time.Unix(hardTTL, 0).Format(time.DateTime))
					
					// Trigger async revalidation in background
					go r.asyncRevalidate(c, req)
				}
			}
			
			// Still return cache hit - serve stale content while refreshing
			return true, nil
		}
	}
	
	// check if metadata is expired (hard expiration).
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

// asyncRevalidate performs background revalidation for fuzzy refresh
func (r *RevalidateProcessor) asyncRevalidate(c *Caching, req *http.Request) {
	// Create a background context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	// Clone the request for background processing
	bgReq := req.Clone(ctx)
	
	// Set conditional headers for revalidation
	if c.md.Headers.Get("ETag") != "" {
		bgReq.Header.Set("If-None-Match", c.md.Headers.Get("ETag"))
	}
	if c.md.Headers.Get("Last-Modified") != "" {
		bgReq.Header.Set("If-Modified-Since", c.md.Headers.Get("Last-Modified"))
	}
	
	// Remove Range header for full object revalidation
	bgReq.Header.Del("Range")
	
	c.log.Debugf("async fuzzy refresh started for object: %s", c.id.Key())
	
	// Perform the upstream request
	resp, err := c.doProxy(bgReq, false)
	if err != nil {
		c.log.Warnf("async fuzzy refresh failed for object %s: %v", c.id.Key(), err)
		return
	}
	defer closeBody(resp)
	
	// Handle 304 Not Modified - just update freshness
	if resp.StatusCode == http.StatusNotModified {
		r.freshness(c, resp)
		c.log.Debugf("async fuzzy refresh completed (304) for object: %s", c.id.Key())
		return
	}
	
	// For non-304 responses, the normal caching logic will handle it
	// through the response body processing
	c.log.Debugf("async fuzzy refresh completed (%d) for object: %s", resp.StatusCode, c.id.Key())
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
