package caching

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/kelindar/bitmap"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/bufio"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/server/middleware"
)

type cachingOption struct{}

func init() {
	middleware.Register("caching", Middleware)
}

func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opts cachingOption
	if err := c.Unmarshal(&opts); err != nil {
		return nil, middleware.EmptyCleanup, err
	}

	processor := NewProcessorChain(
		NewStateProcessor(),
	).fill()

	return func(origin http.RoundTripper) http.RoundTripper {

		proxyClient := proxy.GetProxy()

		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			// find indexdb cache-key has hit/miss.
			caching, err := processor.preCacheProcessor(proxyClient, req)
			// err to BYPASS caching
			if err != nil {
				caching.log.Warnf("caching find failed: %v BYPASS", err)
				return caching.doProxy(req, false)
			}

			// cache HIT
			if caching.hit {
				resp, err = caching.responseCache(req)
				return
			}

			// full MISS
			resp, err = caching.doProxy(req, false)

			processor.postCacheProcessor(caching, req, resp)

			return
		})

	}, middleware.EmptyCleanup, nil
}

type Caching struct {
	log         *log.Helper
	processor   *ProcessorChain
	req         *http.Request
	id          *object.ID
	md          *object.Metadata
	bucket      storage.Bucket
	proxyClient proxy.Proxy

	cacheable    bool
	hit          bool
	refresh      bool
	fileChanged  bool
	noContentLen bool
}

func (c *Caching) responseCache(req *http.Request) (*http.Response, error) {
	// find disk cache file and return Body
	return nil, nil
}

func (c *Caching) doProxy(req *http.Request, subRequest bool) (*http.Response, error) {
	proxyReq, err := c.processor.PreRequst(c, cloneRequest(req))
	if err != nil {
		return nil, fmt.Errorf("pre-request failed: %w", err)
	}

	c.log.Infof("doPorxy with %s", proxyReq.URL.String())

	resp, err := c.proxyClient.Do(proxyReq, false)
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

	var upstreamErr error

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
		if c.md != nil && !c.refresh {
			upstreamErr = fmt.Errorf("upstream returns error status: %d", resp.StatusCode)
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
		flushBuffer, cleanup := c.flushbuffer(respRange)

		// save body stream to bucket(disk).
		resp.Body = bufio.SavepartReader(resp.Body, flushBuffer, cleanup)
	}

	resp, err = c.processor.PostRequst(c, proxyReq, resp)
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
	return resp, upstreamErr
}

// flushbuffer returns flush cache file to bucket callback
func (c *Caching) flushbuffer(respRange *xhttp.ContentRange) (bufio.EventSuccess, bufio.EventClose) {
	// is chunked encoding
	// chunked encoding when object size unknown, waiting for Read io.EOF
	chunked := respRange.ObjSize <= 0

	c.log.Debugf("flushbuffer now. chunked %t", chunked)
	cleanup := func(eof bool) {
		// TODO: close opened file.
	}

	// write file.
	writeBuffer := func(buf []byte, bitIdx uint32, pos uint64, eof bool) error {
		return nil
	}

	return writeBuffer, cleanup
}

// flushFailed flush cache file to bucket failed callback
func (c *Caching) flushFailed(err error) {
	c.log.Errorf("flush body to disk failed: %v", err)
	_ = c.bucket.DiscardWithMetadata(c.req.Context(), c.md)
}

func cloneRequest(req *http.Request) *http.Request {
	proxyURL, _ := url.Parse(req.URL.String())
	if proxyURL.Host == "" {
		proxyURL.Host = req.Host
	}
	if proxyURL.Scheme == "" {
		proxyURL.Scheme = xhttp.Scheme(req)
	}
	proxyReq := &http.Request{
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       req.Host,
		Proto:      req.Proto,
		Method:     req.Method,
		URL:        proxyURL,
		Header:     make(http.Header),
	}

	return proxyReq
}

func newObjectIDFromRequest(req *http.Request, vd string, includeQuery bool) (*object.ID, error) {
	// option: cache-key include querystring
	//
	// TODO: get cache-key from frontend protocol rule.

	// or later default rule.
	if includeQuery {
		return object.NewVirtualID(req.URL.String(), vd), nil
	}

	return object.NewVirtualID(fmt.Sprintf("%s://%s%s", req.URL.Scheme, req.Host, req.URL.Path), vd), nil
}
