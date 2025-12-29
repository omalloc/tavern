package caching

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/kelindar/bitmap"

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
)

const BYPASS = "BYPASS"

var fileMode = os.O_RDONLY

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
	VaryLimit                   int      `json:"vary_limit" yaml:"vary_limit"`
	VaryIgnoreKey               []string `json:"vary_ignore_key" yaml:"vary_ignore_key"`
	Hostname                    string   `json:"hostname" yaml:"hostname"`
}

func init() {
	middleware.Register("caching", Middleware)

	if runtime.GOOS == "linux" {
		fileMode |= 0o1000000 // O_NOATIME
	}
}

// Middleware initializes a middleware component based on the provided configuration and returns the middleware logic.
func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	hostname, _ := os.Hostname()
	opts := &cachingOption{
		VaryLimit:         100,
		Hostname:          hostname,
		ObjectPoolEnabled: false,
		ObjectPollSize:    20000,
		SliceSize:         iobuf.BitBlock, // default 32KB
	}
	if err := c.Unmarshal(opts); err != nil {
		return nil, middleware.EmptyCleanup, err
	}

	log.Infof("middleware.caching inited %v", opts.SliceSize)

	processor := NewProcessorChain(
		// Cache-State
		NewStateProcessor(),
		// Cache Prefetch
		NewPrefetchProcessor(),
		// ETag/Last-Modified If-Match Validation
		NewRevalidateProcessor(),
		// ETag/Last-Modified/ContentLength Changed
		NewFileChangedProcessor(),
		// Vary
		NewVaryProcessor(
			WithVaryMaxLimit(opts.VaryLimit),
			WithVaryIgnoreKeys(opts.VaryIgnoreKey...),
		),
	).fill()

	return func(origin http.RoundTripper) http.RoundTripper {

		proxyClient := proxy.GetProxy()

		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			// find indexdb cache-key has hit/miss.
			caching, err := processor.preCacheProcessor(proxyClient, opts, req)

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

	c.md.LastRefUnix = time.Now().Unix()

	c.log.Debugf("lazilyRespond %s %s start %d end %d", req.Method, c.id.Key(), start, end)

	readers := make([]io.ReadCloser, 0, len(reqChunks))
	_ = func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}

	for _, ichunk := range reqChunks {
		offset, limit := iobuf.ChunkPart(reqChunks, uint32(psize))
		if ichunk == 0 && startOffset > 0 {
			offset += startOffset
		}
		if end < limit {
			limit = end + 1
		}

		// [ 0-32767 = hit, 32768-65535 = miss, 65536-98303 = hit, 98304-131071 = hit]
		// [ from file,       from upstream,     from file   ,      from file ]

		// from cachefile
		// hit block
		// if block.Match {
		// 	readers = append(readers, iobuf.LimitReadCloser(iobuf.SeekReadCloser(f, offset), limit-offset))
		// 	continue
		// }

		// from upstream
		// miss block
		reader, err2 := c.getUpstreamReader(uint64(offset), uint64(limit-1), true)
		if err2 != nil {
			return nil, err2
		}
		readers = append(readers, reader)
	}

	// f, err := ropen(c.id.WPath(c.bucket.Path()))
	// if err != nil {
	// 	if isTooManyFiles(err) {
	// 		c.log.Errorf("too many open files: %v", err)
	// 	}

	// 	// 如果文件不存在，需要回源
	// 	if os.IsNotExist(err) {
	// 		c.log.Warnf("lazilyRespond backoff doProxy with %s", err.Error())
	// 		// 要解除 If-Header 校验304
	// 		req.Header.Del("If-None-Match")
	// 		req.Header.Del("If-Modified-Since")
	// 		req.Header.Del("If-Match")
	// 		req.Header.Del("If-Unmodified-Since")
	// 		req.Header.Del("If-Range")
	// 		// 发起回上游
	// 		return c.doProxy(req, false)
	// 	}
	// 	return nil, err
	// }

	// TODO: find file chunks.

	resp := &http.Response{
		// 状态码可以统一在这里固定 200，由 PostRequest 阶段或 postCacheProcessor 统一处理
		StatusCode:    c.md.Code, // http.StatusOK,
		ContentLength: int64(c.md.Size),
		Header:        make(http.Header),
		// Body:          iobuf.PartsReader(f, readers...),
	}

	xhttp.CopyHeader(resp.Header, c.md.Headers)

	// 206 Range 头处理
	if req.Header.Get("Range") != "" {
		resp.StatusCode = http.StatusPartialContent
		resp.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, c.md.Size))
	}

	// 计算真实 ContentLength, 这里主要防止出现 0 body size 的情况
	cl := end - start + 1
	resp.ContentLength = cl
	resp.Header.Set("Content-Length", strconv.FormatInt(cl, 10))
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
	// TODO: remove all internal header
	req.Header.Del(constants.ProtocolCacheStatusKey)

	doSubRequest := func() (*http.Response, error) {
		now := time.Now()
		c.log.Debugf("getUpstreamReader doProxy[part]: begin: %s, rawRange: %s, newRange: %s", now, rawRange, newRange)
		resp, err := c.doProxy(req, true)
		c.log.Infof("getUpstreamReader doProxy[part]: timeCost: %s, rawRange: %s, newRange: %s", time.Since(now), rawRange, newRange)
		if err != nil {
			closeBody(resp)
			return nil, err
		}
		// 部分命中
		c.cacheStatus = storage.CachePartHit
		// 发起的是 206 请求，但是返回的非 206
		if resp.StatusCode != http.StatusPartialContent {
			c.log.Warnf("getUpstreamReader doProxy[part]: status code: %d, bod size: %d", resp.StatusCode, resp.ContentLength)
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

	// parsed cache-control header
	expiredAt, cacheable := xhttp.ParseCacheTime("", resp.Header)

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

	c.cacheable = cacheable
	// expire time
	c.md.ExpiresAt = now.Add(expiredAt).Unix()
	c.md.RespUnix = now.Unix()
	c.md.LastRefUnix = now.Unix()

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
			copiedHeaders := make(http.Header)
			xhttp.CopyHeader(copiedHeaders, resp.Header)
			c.md.Headers = copiedHeaders
		}

		// flushbuffer 文件从这里写出到 bucket / disk
		flushBuffer, cleanup := c.flushbufferSlice(respRange)

		// save body stream to bucket(disk).
		resp.Body = iobuf.SavepartReader(resp.Body, c.opt.SliceSize, 0, flushBuffer, c.flushFailed, cleanup)
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

	c.log.Debugf("doProxy end %s %q code: %d %s", proxyReq.Method, proxyReq.URL.String(), resp.StatusCode, respRange.String())
	return resp, proxyErr
}

// flushbuffer returns flush cache file to bucket callback
func (c *Caching) flushbuffer(respRange xhttp.ContentRange) (iobuf.EventSuccess, iobuf.EventClose) {
	// is chunked encoding
	// chunked encoding when object size unknown, waiting for Read io.EOF
	chunked := respRange.ObjSize <= 0

	// MAX_FILE_SIZE / PART_SIZE
	// PART_SIZE -> bitmap block_size

	endPart := func() uint32 {
		epart := uint32(respRange.ObjSize / iobuf.BitBlock)
		if respRange.ObjSize%iobuf.BitBlock > 0 {
			epart++
		}
		return epart
	}()

	getOffset := func(partIdx uint32) int64 {
		point := partIdx * iobuf.BitBlock
		if partIdx == 0 {
			point = 0
		}
		return int64(point)
	}

	c.log.Debugf("flushbuffer now. chunked %t", chunked)

	wpath := c.id.WPath(c.bucket.Path())

	if err := os.MkdirAll(filepath.Dir(wpath), 0o755); err != nil {
		c.log.Debugf("mkdir fail %s", err)
	}

	w := bufio.NewWriter(io.Discard)
	f, err := os.OpenFile(wpath, os.O_CREATE|os.O_RDWR, 0o755)
	if err == nil {
		w = bufio.NewWriter(f)
	} else {
		log.Warnf("open-file failed err %s", err)
	}

	cleanup := func(eof bool) {
		_ = c.bucket.Store(c.req.Context(), c.md)
	}

	// TODO: global resource lock.
	// c.Lock()
	// defer c.Unlock()

	// write file.
	writeBuffer := func(buf []byte, bitIdx uint32, current uint64, eof bool) error {
		if chunked {
			c.md.Size = current
			c.md.Headers.Set("Content-Length", fmt.Sprintf("%d", current))
		} else if uint64(len(buf)) != iobuf.BitBlock && current != respRange.ObjSize {
			c.log.Debugf("part[%d] is not complete, want end-part [%d] ", bitIdx+1, endPart)
			return nil
		}

		offset := getOffset(bitIdx)
		if offset > 0 {
			// write buf to `wpath` file at offset
			if _, err = f.Seek(offset, io.SeekStart); err != nil {
				return err
			}
		}

		if nn, err1 := w.Write(buf); err1 != nil || nn != len(buf) {
			return fmt.Errorf("writeBuffer part[%d] failed err %w", bitIdx, err)
		}
		c.md.Parts.Set(bitIdx)

		if eof {
			if endPart == uint32(c.md.Parts.Count()) {
				c.log.Debugf("file all part complete at %s", time.Now().Format(time.DateTime))
				_ = w.Flush()

				// TODO: trigger file crc check
				// ...
			}
		}

		//c.log.Debugf("flushBuffer %s, curPart: %d endPart: %d, offset %d, write bufsize %d", wpath, bitIdx, endPart, offset, n)
		return nil
	}

	return writeBuffer, cleanup
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
		wpath := c.id.WPathSlice(c.bucket.Path(), index)
		_ = os.MkdirAll(filepath.Dir(wpath), 0o755)

		f, err := os.OpenFile(wpath, os.O_CREATE|os.O_RDWR, 0o755)
		if err != nil {
			return fmt.Errorf("writeBuffer open-file part[%d] failed err %w", index, err)
		}
		defer f.Close()

		if chunked {
			c.md.Size = current
			c.md.Headers.Set("Content-Length", fmt.Sprintf("%d", current))
		} else if uint64(len(buf)) != c.md.BlockSize && current != respRange.ObjSize {
			c.log.Debugf("writeBuffer part[%d] is not complete, want end-part [%d] ", index+1, endPart)
			return nil
		}

		if nn, err1 := f.Write(buf); err1 != nil || nn != len(buf) {
			return fmt.Errorf("writeBuffer part[%d] failed err %w", index, err)
		}

		// save slice part
		c.md.Chunks.Set(index)

		if eof {
			if endPart == uint32(c.md.Chunks.Count()) {
				c.log.Debugf("file all part complete at %s", time.Now().Format(time.DateTime))

				// TODO: trigger file crc check
				// ...
			}
		}

		//c.log.Debugf("flushBuffer %s, curPart: %d endPart: %d, offset %d, write bufsize %d", wpath, bitIdx, endPart, offset, n)
		return nil
	}

	return writerBuffer, func(eof bool) {}
}

// flushFailed flush cache file to bucket failed callback
func (c *Caching) flushFailed(err error) {
	c.log.Errorf("flush body to disk failed: %v", err)
	_ = c.bucket.DiscardWithMetadata(c.req.Context(), c.md)
}
