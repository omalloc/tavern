package transform

import (
	"net/http"
	"testing"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/server/middleware"
	planning "github.com/omalloc/tavern/transform"
)

func TestPlannerUsesResolverAndClearsUnmatchedPlans(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a", nil)
	req.Header.Set("Accept", "image/webp")
	req.Header.Set("X-Tavern-Transform-Spec", "forged")
	for _, matched := range []bool{false, true} {
		request := req.WithContext(planning.WithPlan(req.Context(), planning.Plan{SourceURL: "forged-context"}))
		rt := Planner(func(r *http.Request) (planning.Plan, bool, error) {
			return planning.Plan{SourceURL: "https://example.com/source"}, matched, nil
		})(middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			plan, ok := planning.PlanFromContext(r.Context())
			if ok != matched {
				t.Fatal("unmatched route retained plan")
			}
			if matched && (plan.SourceURL != "https://example.com/source" || plan.AcceptHeader.Get("Accept") != "image/webp" || plan.AcceptHeader.Get("X-Tavern-Transform-Spec") != "") {
				t.Fatal(plan)
			}
			return &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody}, nil
		}))
		resp, err := rt.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
}
func TestPublicExecutionWaitsForValidatedBackend(t *testing.T) {
	if _, _, err := Middleware(&configv1.Middleware{Options: map[string]any{"enabled": true}}); err == nil {
		t.Fatal("enabled worker without backend contract")
	}
	mw, cleanup, err := Middleware(&configv1.Middleware{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	called := false
	rt := mw(middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody}, nil
	}))
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/a", nil)
	rt.RoundTrip(req)
	if !called {
		t.Fatal("disabled route did not pass through")
	}
}

func TestPlannerRejectsUnsatisfiableOutputWith406(t *testing.T) {
	called := false
	rt := Planner(func(*http.Request) (planning.Plan, bool, error) {
		return planning.Plan{}, false, planning.ErrNotAcceptable
	})(middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil }))
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/a", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil || resp.StatusCode != 406 || resp.Header.Get("Cache-Control") != "no-store" || called {
		t.Fatal(resp, err)
	}
	resp.Body.Close()
}
