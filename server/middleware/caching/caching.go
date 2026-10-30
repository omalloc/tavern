package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"time"

	"github.com/kelindar/bitmap"

	"github.com/omalloc/tavern/api/defined/v1/event"
	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/pkg/iobuf"
	"github.com/omalloc/tavern/pkg/iobuf/ioindexes"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/server/middleware"
	storagev1 "github.com/omalloc/tavern/storage"
)

const BYPASS = "BYPASS"

var keyMap = map[string]struct{}{
	"Content-Range":  {},
	"Content-Length": {},
}

type Duration string

func (r Duration) AsDuration() time.Duration {
	d, _ := time.ParseDuration(string(r))
	return d
}

type cachingOption struct {
	IncludeQueryInCacheKey      bool     `json:"include_query_in_cache_key" yaml:"include_query_in_cache_key"`
	FuzzyRefresh                bool     `json:"fuzzy_refresh" yaml:"fuzzy_refresh"`
	FuzzyRefreshRate            float64  `json:"fuzzy_refresh_rate" yaml:"fuzzy_refresh_rate"`
	CollapsedRequest            bool     `json:"collapsed_request" yaml:"collapsed_request"`
	CollapsedRequestWaitTimeout Duration `json:"collapsed_request_wait_timeout" yaml:"collapsed_request_wait_timeout"`
	ObjectPoolEnabled           bool     `json:"object_pool_enabled" yaml:"object_pool_enabled"`
	ObjectPollSize              int      `json:"object_poll_size" yaml:"object_poll_size"`
	SliceSize                   uint64   `json:"slice_size" yaml:"slice_size"`
	FillRangePercent            uint64   `json:"fill_range_percent" yaml:"fill_range_percent"`
	VaryLimit                   int      `json:"vary_limit" yaml:"vary_limit"`
	VaryIgnoreKey               []string `json:"vary_ignore_key" yaml:"vary_ignore_key"`
	Hostname                    string   `json:"hostname" yaml:"hostname"`
	AsyncFlushChunk             bool     `json:"async_flush_chunk" yaml:"async_flush_chunk"`
	// Bound internally for controlled representation tests. No external v2 enable switch
	// is exposed until the P2 lifecycle and invalidation protocol is complete.
	representation *representationLookup
	// events.
	publish func(ctx context.Context, payload event.CacheCompleted) `json:"-" yaml:"-"`
}

func init() {
	middleware.Register("caching", Middleware)
}

// Middleware initializes a middleware component based on the provided configuration and returns the middleware logic.
func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	hostname, _ := os.Hostname()
	opts := &cachingOption{
		VaryLimit:         100,
		Hostname:          hostname, // 默认从系统获取主机名, 可通过
		ObjectPoolEnabled: false,
		ObjectPollSize:    20000,
		SliceSize:         1048576, // 切片大小 默认1MB, 从配置文件 storage.slice_size 配置
		FillRangePercent:  100,     // Range 默认填充百分比, 参考 fillRange 处理器对百分比的计算
		AsyncFlushChunk:   false,   // 即刻写出chunk索引 功能（会增加 indexdb io）
	}
	if err := c.Unmarshal(opts); err != nil {
		return nil, middleware.EmptyCleanup, err
	}

	log.Infof("middleware.caching init slice_size %d", opts.SliceSize)
	if len(opts.VaryIgnoreKey) > 0 {
		log.Warn("middleware.caching: vary_ignore_key is deprecated and ignored; legacy Vary caching is bypassed")
	}

	processor := NewProcessorChain(
		// Cache-State
		NewStateProcessor(),
		// Cache Prefetch
		NewPrefetchProcessor(),
		// Vary
		NewVaryProcessor(
			WithVaryMaxLimit(opts.VaryLimit),
			WithVaryIgnoreKeys(opts.VaryIgnoreKey...),
		),
		// ETag/Last-Modified If-Match Validation
		NewRevalidateProcessor(),
		// ETag/Last-Modified/ContentLength Changed
		NewFileChangedProcessor(),
		// Range fill
		NewFillRangeProcessor(
			WithFillRangePercent(int(opts.FillRangePercent)),
			WithChunkSize(opts.SliceSize),
		),
	).fill()

	// register event.
	opts.publish = event.NewPublish[event.CacheCompleted](
		event.NewTopicKey[event.CacheCompleted](event.CacheCompletedKey),
	)

	return func(origin http.RoundTripper) http.RoundTripper {

		proxyClient := proxy.GetProxy()
		store := storagev1.Current()

		// Response sharing stays disabled until followers are checked against
		// the actual origin Vary and cache admission headers (P4.3).

		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			// only cache GET/HEAD request
			if req.Method != http.MethodGet && req.Method != http.MethodHead {
				// discard RequestURI for proxy client
				req.RequestURI = ""
				return proxyClient.Do(req, false, time.Millisecond)
			}

			// find indexdb cache-key has hit/miss.
			caching, err := processor.preCacheProcessor(proxyClient, store, opts, req)
			defer func() {
				if err != nil {
					closeBody(resp)
					caching.finishLegacyLease(nil)
					caching.finishRepresentationLease(nil)
					return
				}
				caching.finishLegacyLease(resp)
				caching.finishRepresentationLease(resp)
			}()

			// TODO: object pool
			// 	reuse (wrapper Body.Close() call object reset and put Pool.)
			//defer func() {
			//	caching.reset()
			//	cachingPool.Put(caching)
			//}()

			// Increment cacheRequestTotal on every response path (BYPASS,
			// HIT, MISS). Placed as a defer so it
			// fires after cacheStatus is finalized.
			defer func() {
				storeType := "none"
				if caching.bucket != nil {
					storeType = caching.bucket.StoreType()
				}
				cacheRequestTotal.WithLabelValues(caching.cacheStatus.String(), storeType).Inc()
			}()

			// err to BYPASS caching
			if err != nil {
				caching.log.Warnf("Precache processor failed: %v BYPASS", err)
				caching.cacheStatus = storage.BYPASS
				caching.bypass = true
				resp, err = caching.doProxy(req, false) // do reverse proxy
				if err != nil {
					return nil, err
				}

				if resp != nil {
					// set cache-staus header BYPASS
					resp.Header.Set(protocol.ProtocolCacheStatusKey, BYPASS)
				}
				return
			}

			// cache HIT
			if caching.hit {
				return caching.respondFromCache(req)
			}

			// A MISS fetches independently: its response Vary is not known yet.
			resp, err = caching.doProxy(req, false)
			if err != nil {
				return nil, err
			}

			resp, err = processor.postCacheProcessor(caching, req, resp)
			return
		})

	}, middleware.EmptyCleanup, nil
}

// respondFromCache assembles a response from cached chunks for a cache HIT.
// It parses the Range header, builds a multi-part reader from disk, and
// runs post-cache processing (headers, cache status, store).
func (c *Caching) respondFromCache(req *http.Request) (*http.Response, error) {
	c.cacheStatus = storage.CacheHit

	rng, err := xhttp.SingleRange(req.Header.Get("Range"), c.md.Size)
	if c.resourceMD != nil {
		rng, err = representationRequestRange(req.Header.Get("Range"), c.md.Size)
	}
	if err != nil {
		headers := make(http.Header)
		xhttp.CopyHeader(c.md.Headers, headers)
		headers.Set("Content-Range", fmt.Sprintf("bytes */%d", c.md.Size))
		return nil, xhttp.NewBizError(http.StatusRequestedRangeNotSatisfiable, headers)
	}

	c.markCacheStatus(rng.Start, rng.End)

	resp, err := c.lazilyRespond(req, rng.Start, rng.End)
	if err != nil {
		closeBody(resp)
		if c.resourceMD != nil && errors.Is(err, os.ErrNotExist) {
			if c.opt.representation == nil || !c.opt.representation.writes {
				c.md, c.variantRef = nil, nil
			}
			c.hit, c.cacheable, c.bypass = false, false, true
			c.cacheStatus = storage.BYPASS
			resp, err = c.doProxy(req, false)
			if err != nil {
				return resp, err
			}
			return c.processor.postCacheProcessor(c, req, resp)
		}
		return nil, err
	}

	return c.processor.postCacheProcessor(c, req, resp)
}

func (c *Caching) lazilyRespond(req *http.Request, start, end int64) (*http.Response, error) {
	// 这里通过缓存的块大小来计算，而不是配置默认的 SliceSize
	// 这样已缓存的对象可以使用原来的配置块大小，不受配置文件变更影响
	psize := c.md.BlockSize
	// 计算请求的 start, end 块索引
	reqChunks := ioindexes.Build(uint64(start), uint64(end), psize)
	startOffset := start % int64(psize)

	hasRangeRequest := req.Header.Get("Range") != ""

	c.md.LastRefUnix = time.Now().Unix()

	c.log.Debugf("lazilyRespond %s %s start %d end %d", req.Method, c.id.Key(), start, end)

	// HEAD reqeust fast check and return.
	if req.Method == http.MethodHead {
		resp := buildNoBodyRespond(c, hasRangeRequest, start, end)
		return resp, nil
	}

	readers := make([]io.ReadCloser, 0, len(reqChunks))

	for i := 0; i < len(reqChunks); {
		reader, count, err := getContents(c, reqChunks, uint32(i))
		if err != nil {
			iobuf.AllCloser(readers).Close()
			return nil, err
		}

		if count == -1 {
			iobuf.AllCloser(readers).Close()
			readers = []io.ReadCloser{
				iobuf.RangeReader(reader, 0, int(c.md.Size-1), int(start), int(end)),
			}
			break
		}

		// head skip
		if i == 0 && startOffset > 0 {
			reader = iobuf.SkipReadCloser(reader, int64(startOffset))
		}

		// tail skip
		if i+count == len(reqChunks) {
			endLimit := uint64(count-1)*psize + uint64(end)%psize + 1
			if i == 0 {
				endLimit -= uint64(startOffset)
			}
			reader = iobuf.LimitReadCloser(reader, int64(endLimit))
		}

		readers = append(readers, reader)
		i += count
	}

	in := iobuf.PartsReadCloser(iobuf.AllCloser(readers), readers...)

	resp := buildNoBodyRespond(c, hasRangeRequest, start, end)
	resp.Body = in

	return resp, nil
}

func (c *Caching) getUpstreamReader(fromByte, toByte uint64, async bool) (io.ReadCloser, error) {
	// get from origin request header
	rawRange := c.req.Header.Get("Range")

	// Every missing range fetches independently; response headers have not
	// yet established whether another request could safely reuse its bytes.
	doSubRequest := func(unionFrom, unionTo uint64) (*http.Response, error) {
		newRange := fmt.Sprintf("bytes=%d-%d", unionFrom, unionTo)
		subReq := c.req.Clone(c.req.Context())
		subReq.Header.Set("Range", newRange)
		subReq.Header.Del(protocol.ProtocolCacheStatusKey)
		// A background fetch owns its metadata and processor state. Do not
		// change the object the foreground reader is still assembling.
		sub := *c
		sub.md = c.md.Clone()
		sub.req = subReq

		now := time.Now()
		c.log.Debugf("getUpstreamReader doProxy[chunk]: begin: %s, rawRange: %s, newRange: %s", now, rawRange, newRange)
		resp, err := sub.doProxy(subReq, true)
		c.log.Infof("getUpstreamReader doProxy[chunk]: timeCost: %s, rawRange: %s, newRange: %s", time.Since(now), rawRange, newRange)
		if err != nil {
			closeBody(resp)
			return nil, err
		}
		// 206 Partial Content is expected for range requests.
		// If we get a different status code, it may indicate an issue with the upstream response.
		if resp.StatusCode != http.StatusPartialContent {
			c.log.Warnf("getUpstreamReader doProxy[chunk]: status code: %d, bod size: %d", resp.StatusCode, resp.ContentLength)
			return resp, xhttp.NewBizError(resp.StatusCode, resp.Header)
		}
		return resp, nil
	}

	// The old object ID alone cannot prove a new range response belongs
	// to this representation. Fetch independently until generation validation.
	if async {
		return iobuf.AsyncReadCloser(func() (*http.Response, error) {
			return doSubRequest(fromByte, toByte)
		}), nil
	}

	resp, err := doSubRequest(fromByte, toByte)
	if resp != nil {
		return resp.Body, err
	}
	return nil, err
}

func (c *Caching) doProxy(req *http.Request, subRequest bool) (*http.Response, error) {
	if c.opt.representation != nil && c.opt.representation.writes {
		return c.proxyRepresentation(req)
	}
	proxyReq := cloneRequest(req)
	var err error
	if !c.bypass {
		proxyReq, err = c.processor.PreRequest(c, proxyReq)
		if err != nil {
			return nil, fmt.Errorf("pre-request failed: %w", err)
		}
	}

	c.log.Debugf("doProxy begin with %s", proxyReq.URL.String())

	resp, err := c.executeUpstream(proxyReq)
	if err != nil {
		return resp, err
	}

	if c.bypass {
		return resp, nil
	}

	// Decide admission before creating a body writer or running processors
	// that could turn an origin 304 into a cached 200.
	if !admitResponse(proxyReq, resp) {
		revalidating := c.revalidate
		c.bypass = true
		c.cacheable = false
		c.cacheStatus = storage.BYPASS
		c.revalidate = false
		if c.md != nil {
			_ = c.bucket.DiscardWithMessage(req.Context(), c.id, "response no longer eligible for shared caching")
		}
		c.md = nil
		c.rootmd = nil
		if subRequest {
			// Do not splice a response with changed selection or shared-cache
			// policy into the headers and chunks of the old representation.
			closeBody(resp)
			return nil, errors.New("range fill response is no longer eligible for shared caching")
		}
		if revalidating && resp.StatusCode == http.StatusNotModified {
			// The cached body can no longer satisfy this validation. Retry
			// once using the client's original conditions, without our own.
			closeBody(resp)
			return c.doProxy(req, subRequest)
		}
		// A pre-request Range expansion still needs client-side trimming.
		return c.processor.PostRequest(c, proxyReq, resp)
	}

	c.log.Debugf("doProxy upstream resp content-length %d content-range %s etag %q lm %q",
		resp.ContentLength, resp.Header.Get("Content-Range"),
		resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))

	if log.Enabled(log.LevelDebug) {
		buf, _ := httputil.DumpResponse(resp, false)
		c.log.Debugf("doProxy resp dump: \n%s\n", string(buf))
	}

	var proxyErr error

	// handle redirect caching
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		// origin response
		c.log.Debugf("doProxy upstream returns 301/302 url: %s location: %s",
			proxyReq.URL.String(), resp.Header.Get("Location"))
		return resp, nil
	}

	// handle Range Not Satisfiable
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// errors.New("upstream returns 416 Range Not Satisfiable")
		return resp, xhttp.NewBizError(resp.StatusCode, resp.Header)
	}

	// handle error response
	if resp.StatusCode >= http.StatusBadRequest {
		if c.md != nil && !c.revalidate {
			proxyErr = fmt.Errorf("upstream returns error status: %d", resp.StatusCode)
		}
	}

	// code check
	notModified := resp.StatusCode == http.StatusNotModified
	statusOK := resp.StatusCode == http.StatusOK

	respRange, err := xhttp.ParseContentRange(resp.Header)
	if !notModified && !statusOK && err != nil && !errors.Is(err, xhttp.ErrContentRangeHeaderNotFound) {
		c.log.Errorf("doProxy parse upstream Content-Range header failed: %v", err)
		return resp, err
	}

	if err != nil {
		c.noContentLen = true
	}

	now := time.Now()
	if c.md == nil {
		c.md = &object.Metadata{
			ID:          c.id,
			Headers:     make(http.Header),
			BlockSize:   c.opt.SliceSize, // iobuf.BitBlock,
			Parts:       bitmap.Bitmap{},
			Size:        respRange.ObjSize,
			Code:        http.StatusOK,
			RespUnix:    now.Unix(),
			LastRefUnix: now.Unix(),
		}
	}

	// parsed cache-control header
	expiredAt, cacheable := xhttp.ParseCacheTime(protocol.ProtocolCacheTime, resp.Header)

	// expire time
	c.md.ExpiresAt = now.Add(expiredAt).Unix()
	c.md.RespUnix = now.Unix()
	c.md.LastRefUnix = now.Unix()
	c.cacheable = cacheable

	// file changed.
	if !notModified {

		xhttp.RemoveHopByHopHeaders(resp.Header)

		statusCode := resp.StatusCode
		if statusCode == http.StatusPartialContent {
			statusCode = http.StatusOK
		}
		c.md.Code = statusCode
		c.md.Size = respRange.ObjSize

		// error code cache feature.
		if statusCode >= http.StatusBadRequest {

			// Caching is disabled
			// restoring the default behavior for error codes.
			if resp.Header.Get(protocol.InternalCacheErrCode) != protocol.FlagOn {
				c.cacheable = false

				copiedHeaders := make(http.Header)
				xhttp.CopyHeader(copiedHeaders, resp.Header)
				c.md.Headers = copiedHeaders
			} else {
				// Caching is allowed (or rather, not disabled),
				// and the proxy error is suppressed
				proxyErr = nil
			}
		}

	}

	// Keep the origin stream inside any client-side Range wrapper. Install a
	// writer only after processors have finalized the stored metadata.
	originHeaders := resp.Header.Clone()
	originBody := &struct{ io.ReadCloser }{resp.Body}
	if !notModified {
		resp.Body = originBody
	}

	resp, err = c.processor.PostRequest(c, proxyReq, resp)
	if err != nil {
		return resp, err
	}

	// upgrade to chunked type
	if c.noContentLen && statusOK {
		c.md.Flags |= object.FlagChunkedCache
	}

	// update indexdb headers
	if !notModified && (c.fileChanged || !subRequest) {
		c.md.Headers = originHeaders
		c.md.Headers.Del("Content-Range")
		if !c.noContentLen {
			c.md.Headers.Set("Content-Length", fmt.Sprint(c.md.Size))
		}
	}

	// drop internal header
	c.md.Headers.Del("X-Protocol")
	c.md.Headers.Del("X-Protocol-Cache")
	c.md.Headers.Del("X-Protocol-Request-Id")

	if !notModified && c.cacheable && req.Method != http.MethodHead {
		session := c.newWriteSession()
		flush, cleanup := flushbufferSlice(session, respRange)
		originBody.ReadCloser = iobuf.SavepartAsyncReader(originBody.ReadCloser, session.md.BlockSize, uint(respRange.Start), flush, session.fail, cleanup, 8)
	}

	c.log.Debugf("doProxy end %s %q code: %d %s", proxyReq.Method, proxyReq.URL.String(), resp.StatusCode, respRange.String())
	return resp, proxyErr
}
