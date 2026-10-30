package transform

import (
	"fmt"
	"net/http"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/server/middleware"
	planning "github.com/omalloc/tavern/transform"
)

func init() { middleware.Register("transform", Middleware) }

// The standalone factory supports pass-through. Enabled execution is assembled
// by the server with the caching owner and a successfully verified runtime.
func Middleware(config *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opt option
	if err := config.Unmarshal(&opt); err != nil {
		return nil, middleware.EmptyCleanup, err
	}
	if opt.Enabled {
		return nil, middleware.EmptyCleanup, fmt.Errorf("transform execution requires a validated server caching/provider binding")
	}
	return middleware.EmptyMiddleware, middleware.EmptyCleanup, nil
}

type Resolver func(*http.Request) (planning.Plan, bool, error)

// Planner is the controlled plan entry point, outside caching and multirange.
// A later source request can explicitly drop its plan to avoid route recursion.
func Planner(resolve Resolver) middleware.Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			plan, matched, err := resolve(req)
			if err != nil {
				status := planning.HTTPStatus(err)
				if status == http.StatusInternalServerError {
					return nil, err
				}
				return planning.ErrorResponse(req, status), nil
			}
			ctx := planning.WithoutPlan(req.Context())
			if matched {
				plan.AcceptHeader = planning.ClientHeaders(req)
				ctx = planning.WithPlan(ctx, plan)
			}
			return next.RoundTrip(req.WithContext(ctx))
		})
	}
}

func WithRuntime(runtime *planning.Runtime) (middleware.Middleware, error) {
	if runtime != nil && !runtime.Enabled {
		return middleware.EmptyMiddleware, nil
	}
	if runtime == nil || runtime.Router == nil || runtime.Worker == nil || !runtime.Worker.Verified() {
		return nil, fmt.Errorf("transform runtime has no verified backend")
	}
	return Planner(func(req *http.Request) (planning.Plan, bool, error) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			return planning.Plan{}, false, nil
		}
		return runtime.Router.Resolve(req)
	}), nil
}
