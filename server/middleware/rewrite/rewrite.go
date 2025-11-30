package rewrite

import (
	"net/http"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/server/middleware"
)

type HeadersPolicy struct {
	Set    map[string]string `yaml:"set,omitempty"`
	Add    map[string]string `yaml:"add,omitempty"`
	Remove []string          `yaml:"remove,omitempty"`
}

type middlewareOption struct {
	RequestHeadersRewrite  *HeadersPolicy `yaml:"request_headers_rewrite,omitempty"`
	ResponseHeadersRewrite *HeadersPolicy `yaml:"response_headers_rewrite,omitempty"`
}

func init() {
	middleware.Register("rewrite", Middleware)
}

func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opts middlewareOption
	if err := c.Unmarshal(&opts); err != nil {
		return nil, nil, err
	}

	return func(origin http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// Rewrite request headers
			if opts.RequestHeadersRewrite != nil {
				for k, v := range opts.RequestHeadersRewrite.Set {
					req.Header.Set(k, v)
				}
				for k, v := range opts.RequestHeadersRewrite.Add {
					req.Header.Add(k, v)
				}
				for _, k := range opts.RequestHeadersRewrite.Remove {
					req.Header.Del(k)
				}
			}

			resp, err := origin.RoundTrip(req)

			// Rewrite response headers
			if err == nil && opts.ResponseHeadersRewrite != nil {
				for k, v := range opts.ResponseHeadersRewrite.Set {
					resp.Header.Set(k, v)
				}
				for k, v := range opts.ResponseHeadersRewrite.Add {
					resp.Header.Add(k, v)
				}
				for _, k := range opts.ResponseHeadersRewrite.Remove {
					resp.Header.Del(k)
				}
			}

			return resp, err
		})
	}, middleware.EmptyCleanup, nil
}
