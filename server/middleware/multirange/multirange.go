package multirange

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/contrib/log"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/server/middleware"
)

type middlewareOption struct{}

func init() {
	middleware.Register("multirange", Middleware)
}

func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opts middlewareOption
	if err := c.Unmarshal(&opts); err != nil {
		return nil, nil, err
	}

	cleanup := func() {}

	return func(origin http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			// parse Range header
			rawRange := req.Header.Get("Range")
			if rawRange == "" {
				return origin.RoundTrip(req)
			}

			unsafeRanges, err := xhttp.UnsatisfiableMultiRange(rawRange)
			if err != nil || len(unsafeRanges) <= 1 {
				return origin.RoundTrip(req)
			}

			header := make(http.Header)

			head, err1 := prefetchResource(req, origin)
			if err1 != nil {
				return nil, err1
			}
			if head.Body != nil {
				_ = head.Body.Close()
			}
			objSize := uint64(head.ContentLength)
			ctype := head.Header.Get("Content-Type")
			xhttp.CopyHeadersWithout(header, head.Header, "Content-Length", "Content-Range", "Content-Type")

			ranges, err2 := xhttp.Parse(rawRange, objSize)
			if err2 != nil {
				return nil, err2
			}
			sendSize := rangesMIMESize(ranges, ctype, objSize)
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)

			go func() {
				for _, ra := range ranges {
					part, err3 := mw.CreatePart(ra.MimeHeader(ctype, objSize))
					if err3 != nil {
						log.Errorf("create part failed: %s", err3)
						_ = pw.CloseWithError(err3)
						return
					}

					workerRequest := req.Clone(context.Background())
					workerRequest.Header.Set("Range", ra.String())
					raResp, err3 := origin.RoundTrip(workerRequest)
					if err3 != nil {
						_ = pw.CloseWithError(err3)
						return
					}
					if _, err3 = io.Copy(part, raResp.Body); err3 != nil {
						_ = pw.CloseWithError(err3)
						return
					}
				}
				_ = mw.Close()
				_ = pw.Close()
			}()

			// 写入响应头部
			header.Set("Accept-Ranges", "bytes")
			header.Set("Content-Length", strconv.FormatInt(sendSize, 10))
			header.Set("Content-Type", mw.FormDataContentType())
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Header:     header,
				Body:       pr,
			}, nil
		})
	}, cleanup, nil
}

// 发起 HEAD 请求获取资源信息; content-type, content-length
func prefetchResource(req *http.Request, next http.RoundTripper) (*http.Response, error) {
	newreq := req.Clone(context.Background())
	newreq.Header.Del("Range")
	newreq.Method = http.MethodHead
	head, err1 := next.RoundTrip(newreq)
	if err1 != nil {
		backoff := func(oldr *http.Request) (*http.Response, error) {
			nreq := oldr.Clone(context.Background())
			nreq.Method = http.MethodGet
			return next.RoundTrip(newreq)
		}
		return backoff(req)
	}
	return head, err1
}
