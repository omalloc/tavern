package transform

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/server/middleware"
	planning "github.com/omalloc/tavern/transform"
)

func init() { middleware.Register("transform", Middleware) }

type option struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// Public execution stays closed until provider/worker contract validation is
// connected. Merely configuring this middleware must never claim conversion.
func Middleware(config *configv1.Middleware) (middleware.Middleware, func(), error) {
	var opt option
	if err := config.Unmarshal(&opt); err != nil {
		return nil, middleware.EmptyCleanup, err
	}
	if opt.Enabled {
		return nil, middleware.EmptyCleanup, fmt.Errorf("transform execution requires the pending validated provider/worker binding")
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
				body := http.StatusText(status)
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}, "Cache-Control": {"no-store"}}, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}, nil
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
