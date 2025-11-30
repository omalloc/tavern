package caching

import (
	"net/http"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/server/middleware"
)

type cachingOption struct{}

func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opts cachingOption
	if err := c.Unmarshal(&opts); err != nil {
		return nil, middleware.EmptyCleanup, err
	}

	processor := NewProcessorChain(
		NewStateProcessor(),
	).fill()

	return func(origin http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			// find indexdb cache-key has hit/miss.
			caching, err := processor.preCacheProcessor(req)
			// err to BYPASS caching
			if err != nil {
				caching.log.Warnf("caching find failed: %v BYPASS", err)
				return caching.doProxy(req)
			}

			return nil, nil
		})

	}, middleware.EmptyCleanup, nil
}

type Caching struct {
	log       *log.Helper
	processor *ProcessorChain
	req       *http.Request

	hit         bool
	refresh     bool
	fileChanged bool
}

func (c *Caching) doProxy(req *http.Request) (*http.Response, error) {
	return nil, nil
}
