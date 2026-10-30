package transform

import (
	"io"
	"net/http"
	"testing"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/server/middleware"
	planning "github.com/omalloc/tavern/transform"
)

func TestPausedManagedRoutesPreserveOutputContractAndOrdinaryProxy(t *testing.T) {
	caps := transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: []string{"image/jpeg"}, Outputs: []transformv1.Codec{{MIME: "image/png", Lossless: true, CompressionMax: 9}}, Conversions: []transformv1.Conversion{{InputMIME: "image/jpeg", OutputMIME: "image/png"}}, PolicyVersion: "worker-p1", EncoderVersion: "worker-e1"}
	router, err := planning.NewRouter([]planning.Route{{PathPrefix: "/images/", Policy: transformv1.Policy{Formats: []string{"image/png"}, PolicyVersion: "route-p1"}}}, caps)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := WithRuntime(&planning.Runtime{Router: router})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	rt := mw(middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if _, planned := planning.PlanFromContext(req.Context()); planned {
			t.Error("paused pass-through retained a plan")
		}
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody}, nil
	}))
	for _, method := range []string{"GET", "HEAD"} {
		req, _ := http.NewRequestWithContext(t.Context(), method, "https://example.com/images/a?format=png", nil)
		req.Header.Set("Accept", "image/png")
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 503 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Vary") == "" || calls != 0 || (method == "HEAD" && len(body) != 0) {
			t.Fatal("paused route relaxed its output contract", resp, err)
		}
	}
	for _, item := range []struct{ method, path string }{{"GET", "/ordinary"}, {"PURGE", "/images/a?format=png"}} {
		req, _ := http.NewRequestWithContext(t.Context(), item.method, "https://example.com"+item.path, nil)
		req = req.WithContext(planning.WithPlan(req.Context(), planning.Plan{SourceURL: "stale"}))
		resp, err := rt.RoundTrip(req)
		if err != nil || resp.StatusCode != 204 {
			t.Fatal(resp, err)
		}
		resp.Body.Close()
	}
	if calls != 2 {
		t.Fatal("ordinary or PURGE path stopped", calls)
	}
}

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

func TestPrepareValidatesFlattenedRoutePolicyAndRetainsRollbackMapping(t *testing.T) {
	caps := transformv1.BackendCapabilities{SchemaVersion: 1, Inputs: []string{"image/jpeg"}, Outputs: []transformv1.Codec{{MIME: "image/png", Lossless: true, CompressionMax: 9}}, Conversions: []transformv1.Conversion{{InputMIME: "image/jpeg", OutputMIME: "image/png"}}, PolicyVersion: "worker-p1", EncoderVersion: "worker-e1"}
	makeConfig := func() *configv1.Middleware {
		return &configv1.Middleware{Options: map[string]any{"enabled": true, "routes": []map[string]any{{"path_prefix": "/images/", "mode": "auto", "formats": []string{"image/png"}, "policy_version": "route-p1", "quality": 91, "fallback_to_source": true}}, "backend": map[string]any{"name": "http", "endpoint": "http://127.0.0.1:1/v1/transform", "capabilities": caps}, "limits": map[string]any{"max_input_bytes": 1024, "max_output_bytes": 2048, "max_pixels": 256, "concurrency": 1, "queue_size": 1, "timeout": "1s"}}}
	}
	config := makeConfig()
	runtime, err := Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a?format=png", nil)
	plan, matched, err := runtime.Router.Resolve(req)
	if err != nil || !matched || plan.Policy.Quality != 91 || plan.Policy.PolicyVersion != "route-p1" || !plan.FallbackToSource {
		t.Fatal("flat policy lost", plan, err)
	}
	if _, err = WithRuntime(runtime); err == nil {
		t.Fatal("unverified runtime accepted")
	}
	config = makeConfig()
	config.Options["routes"].([]map[string]any)[0]["formats"] = []string{"image/webp"}
	if invalid, err := Prepare(config); err == nil {
		invalid.Close()
		t.Fatal("undeclared route output accepted")
	}
	config = makeConfig()
	config.Options["enabled"] = false
	config.Options["backend"].(map[string]any)["endpoint"] = "unavailable://worker"
	rollback, err := Prepare(config)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback.Close()
	if rollback.Enabled || rollback.Worker != nil || rollback.Router == nil {
		t.Fatal("rollback did not retain pure mapping")
	}
	if _, err = WithRuntime(rollback); err != nil {
		t.Fatal(err)
	}
	mw, cleanup, err := Middleware(config)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	rt := mw(middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("standalone paused route reached raw origin")
		return nil, nil
	}))
	req, _ = http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a?format=png", nil)
	response, err := rt.RoundTrip(req)
	if err != nil || response.StatusCode != 503 {
		t.Fatal(response, err)
	}
	response.Body.Close()
}
