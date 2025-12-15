package caching

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/omalloc/tavern/internal/constants"
	"github.com/omalloc/tavern/pkg/iobuf"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

var _ Processor = (*PrefetchProcessor)(nil)

type prefetchRangeKey struct{}

type PrefetchOption func(r *PrefetchProcessor)

type PrefetchProcessor struct{}

func NewPrefetchProcessor(opts ...PrefetchOption) Processor {
	p := &PrefetchProcessor{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (r *PrefetchProcessor) Lookup(c *Caching, req *http.Request) (bool, error) {
	// skip check
	return true, nil
}

func (r *PrefetchProcessor) PreRequest(c *Caching, req *http.Request) (*http.Request, error) {
	if key := req.Header.Get(constants.PrefetchCacheKey); key != "" {
		if rawRange := req.Header.Get("Range"); rawRange != "" {
			req.Header.Del("Range")
			req = req.WithContext(context.WithValue(req.Context(), prefetchRangeKey{}, rawRange))
		}
		c.prefetch = true
		req.Header.Del(constants.PrefetchCacheKey)
		c.log.Debugf("prefetch request: %s %s", req.Method, req.URL.String())
	}
	return req, nil
}

func (r *PrefetchProcessor) PostRequest(c *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	if c.prefetch {
		// case1: 200 + content-length
		// case2: 200 + chunked
		if resp.StatusCode != http.StatusOK {
			return resp, nil
		}

		sizeof, err := strconv.Atoi(resp.Header.Get("Content-Length"))
		if err != nil {
			c.log.Warnf("parsed content-length error: %s, maybe a chunked response", err)
		}

		rawRange := req.Context().Value(prefetchRangeKey{}).(string)
		if rawRange != "" {
			rng, _ := xhttp.SingleRange(rawRange, uint64(sizeof))
			resp.Body = iobuf.RangeReader(resp.Body, 0, sizeof, int(rng.Start), int(rng.End))
			resp.StatusCode = http.StatusPartialContent
			resp.Header.Set("Content-Range", rng.ContentRange(uint64(sizeof)))
			resp.Header.Set("Content-Length", fmt.Sprintf("%d", rng.Length()))
		}
		c.log.Debugf("prefetch response: %s, content-length: %d, raw-range: %s", req.URL.String(), sizeof, rawRange)
	}
	return resp, nil
}
