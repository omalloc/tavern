package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"time"

	"github.com/kelindar/bitmap"

	"github.com/omalloc/tavern/api/defined/v1/event"
	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/constants"
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

		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			// only cache GET/HEAD request
			if req.Method != http.MethodGet && req.Method != http.MethodHead {
				// discard RequestURI for proxy client
				req.RequestURI = ""
				return proxyClient.Do(req, false, time.Millisecond)
			}

			// find indexdb cache-key has hit/miss.
			caching, err := processor.preCacheProcessor(proxyClient, store, opts, req)

			// TODO: object pool
			// 	reuse (wrapper Body.Close() call object reset and put Pool.)
			//defer func() {
			//	caching.reset()
			//	cachingPool.Put(caching)
			//}()

			// err to BYPASS caching
			if err != nil {
				caching.log.Warnf("Precache processor failed: %v BYPASS", err)
				resp, err = caching.doProxy(req, false) // do reverse proxy
				if err != nil {
					return nil, err
				}

				if resp != nil {
					// set cache-staus header BYPASS
					resp.Header.Set(constants.ProtocolCacheStatusKey, BYPASS)
				}
				return
			}

			// cache HIT
			if caching.hit {
				caching.cacheStatus = storage.CacheHit

				rng, err1 := xhttp.SingleRange(req.Header.Get("Range"), caching.md.Size)
				if err1 != nil {
					// 无效 Range 处理
					headers := make(http.Header)
					xhttp.CopyHeader(caching.md.Headers, headers)
					headers.Set("Content-Range", fmt.Sprintf("bytes */%d", caching.md.Size))
					return nil, xhttp.NewBizError(http.StatusRequestedRangeNotSatisfiable, headers)
				}

				// mark cache status with Range requests.
				caching.markCacheStatus(rng.Start, rng.End)

				// find file seek(start, end)
				resp, err = caching.lazilyRespond(req, rng.Start, rng.End)
				if err != nil {
					// fd leak
					closeBody(resp)
					return nil, err
				}

				// response now
				resp, err = caching.processor.postCacheProcessor(caching, req, resp)
				return
			}

			// full MISS
			resp, err = caching.doProxy(req, false)
			if err != nil {
				return nil, err
			}

			resp, err = processor.postCacheProcessor(caching, req, resp)
			return
		})

	}, middleware.EmptyCleanup, nil
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
	newRange := fmt.Sprintf("bytes=%d-%d", fromByte, toByte)
	req := c.req.Clone(context.Background())
	req.Header.Set("Range", newRange)
	// add request-id [range]
	// req.Header.Set("X-Request-ID", fmt.Sprintf("%s-%d", req.Header.Get(appctx.ProtocolRequestIDKey), fromByte)) // 附加 Request-ID suffix

	// remove all internal header
	req.Header.Del(constants.ProtocolCacheStatusKey)

	doSubRequest := func() (*http.Response, error) {
		now := time.Now()
		c.log.Debugf("getUpstreamReader doProxy[chunk]: begin: %s, rawRange: %s, newRange: %s", now, rawRange, newRange)
		resp, err := c.doProxy(req, true)
		c.log.Debugf("getUpstreamReader doProxy[chunk]: timeCost: %s, rawRange: %s, newRange: %s", time.Since(now), rawRange, newRange)
		if err != nil {
			closeBody(resp)
			return nil, err
		}
		// 部分命中
		c.cacheStatus = storage.CachePartHit
		// 发起的是 206 请求，但是返回的非 206
		if resp.StatusCode != http.StatusPartialContent {
			c.log.Warnf("getUpstreamReader doProxy[chunk]: status code: %d, bod size: %d", resp.StatusCode, resp.ContentLength)
			return resp, xhttp.NewBizError(resp.StatusCode, resp.Header)
		}
		return resp, nil
	}

	if async {
		return iobuf.AsyncReadCloser(doSubRequest), nil
	}

	resp, err := doSubRequest()
	if resp != nil {
		return resp.Body, err
	}
	return nil, err
}

func (c *Caching) doProxy(req *http.Request, subRequest bool) (*http.Response, error) {
	proxyReq, err := c.processor.PreRequest(c, cloneRequest(req))
	if err != nil {
		return nil, fmt.Errorf("pre-request failed: %w", err)
	}

	c.log.Debugf("doProxy begin with %s", proxyReq.URL.String())

	resp, err := c.proxyClient.Do(proxyReq, c.opt.CollapsedRequest, c.opt.CollapsedRequestWaitTimeout.AsDuration())
	if err != nil {
		return resp, err
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
	expiredAt, cacheable := xhttp.ParseCacheTime(constants.CacheTime, resp.Header)

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
			if resp.Header.Get(constants.InternalCacheErrCode) != constants.FlagOn {
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

		// `cacheable` means can write to cache storage
		if c.cacheable {
			// flushbuffer 文件从这里写出到 bucket / disk
			flushBuffer, cleanup := c.flushbufferSlice(respRange)

			// save body stream to bucket(disk).
			resp.Body = iobuf.SavepartAsyncReader(resp.Body, c.md.BlockSize, uint(respRange.Start), flushBuffer, c.flushFailed, cleanup, 8)
		}

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
	if c.fileChanged || !subRequest {
		xhttp.CopyHeader(c.md.Headers, resp.Header)
	}

	// drop internal header
	c.md.Headers.Del("X-Protocol")
	c.md.Headers.Del("X-Protocol-Cache")
	c.md.Headers.Del("X-Protocol-Request-Id")

	c.log.Debugf("doProxy end %s %q code: %d %s", proxyReq.Method, proxyReq.URL.String(), resp.StatusCode, respRange.String())
	return resp, proxyErr
}

func (c *Caching) flushbufferSlice(respRange xhttp.ContentRange) (iobuf.EventSuccess, iobuf.EventClose) {
	// is chunked encoding
	// chunked encoding when object size unknown, waiting for Read io.EOF
	chunked := respRange.ObjSize <= 0

	// auto calculate end part with block-size.
	endPart := func() uint32 {
		epart := uint32(respRange.ObjSize / c.md.BlockSize)
		if respRange.ObjSize%c.md.BlockSize > 0 {
			epart++
		}
		return epart
	}()

	writerBuffer := func(buf []byte, index uint32, current uint64, eof bool) error {
		f, wpath, err := c.bucket.WriteChunkFile(c.req.Context(), c.id, index)
		if err != nil {
			return err
		}
		defer func() {
			if err := f.Close(); err != nil {
				_ = err
			}

			// push verifier
			if eof && endPart == uint32(c.md.Chunks.Count()) {
				c.log.Debug("file all chunk complete")

				// trigger file crc check
				// has InMemory store type skip crc check
				if c.bucket.StoreType() == storage.TypeInMemory {
					return
				}

				c.opt.publish(context.Background(), &cacheCompleted{
					ratio:         0, // 0 = use verifier plugin ratio, 0 > = percent sampling, -1 = disable
					storeUrl:      c.id.Key(),
					storeKey:      c.id.HashStr(),
					storePath:     filepath.Dir(wpath),
					lastModified:  c.md.Headers.Get("Last-Modified"),
					contentLength: int64(c.md.Size),
					chunkCount:    c.md.Chunks.Count(),
					chunkSize:     c.md.BlockSize,
				})
			}
		}()

		if chunked {
			c.md.Size = current
			c.md.Headers.Set("Content-Length", fmt.Sprintf("%d", current))
		} else if uint64(len(buf)) != c.md.BlockSize && current != respRange.ObjSize {
			c.log.Debugf("writeBuffer chunk[%d] is not complete, want end chunk [%d] ", index+1, endPart)
			return nil
		}

		c.log.Debugf("flushBuffer wpath=%s isChunked=%t fileChunk=%d/%d", wpath, chunked, index+1, endPart)

		if nn, err1 := f.Write(buf); err1 != nil || nn != len(buf) {
			return fmt.Errorf("writeBuffer wpath[%s] chunk[%d] failed nn[%d] want[%d] err %v", wpath, index+1, nn, len(buf), err1)
		}

		// save slice chunk
		c.md.Chunks.Set(index)

		if !c.opt.AsyncFlushChunk {
			// store chunk now.
			_ = c.bucket.Store(c.req.Context(), c.md)
		}

		return nil
	}

	writerCloser := func(eof bool) {
		if !eof && chunked {
			_ = c.bucket.DiscardWithMessage(c.req.Context(), c.id, "incomplete chunked file discard")
			return
		}

		if c.opt.AsyncFlushChunk {
			// store chunk with last eof event.
			_ = c.bucket.Store(c.req.Context(), c.md)
		}

	}

	return writerBuffer, writerCloser
}

// flushFailed flush cache file to bucket failed callback
func (c *Caching) flushFailed(err error) {
	c.log.Errorf("flush body to disk failed: %v", err)
	_ = c.bucket.DiscardWithMetadata(c.req.Context(), c.md)
}
