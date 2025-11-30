package recovery

import (
	"net/http"
	"sync/atomic"
	"time"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/x/runtime"
	"github.com/omalloc/tavern/server/middleware"
)

type middlewareOption struct {
	FailCountThreshold int64 `json:"fail_count_threshold,omitempty" yaml:"fail_count_threshold,omitempty"`
	FailWindow         int32 `json:"fail_window,omitempty" yaml:"fail_window,omitempty"`
}

func init() {
	middleware.Register("recovery", Middleware)
}

func Middleware(c *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opts middlewareOption
	if err := c.Unmarshal(&opts); err != nil {
		return nil, nil, err
	}

	var failCount atomic.Int32

	stopCh := make(chan struct{}, 1)
	tick := func() {
		windowTicker := time.NewTicker(time.Duration(opts.FailWindow) * time.Second)

		for {
			select {
			case <-windowTicker.C:
				failCount.Store(0)
			case <-stopCh:
				return
			}
		}
	}

	go tick()

	return func(origin http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (resp *http.Response, err error) {
			defer func() {
				if r := recover(); r != nil {
					// Here you can log the panic or handle it as needed
					log.Context(req.Context()).Errorf("middleware recovery: %s \n%s", r, runtime.PrintStackTrace(4))

					failCount.Add(1)
					if failCount.Load() >= int32(opts.FailCountThreshold) {
						log.Context(req.Context()).Errorf("middleware recovery: reached fail count threshold (%d), healthy now fail.", opts.FailCountThreshold)
						// TODO: trigger healthy fail
					}
				}
			}()

			resp, err = origin.RoundTrip(req)

			return
		})
	}, middleware.EmptyCleanup, nil
}
