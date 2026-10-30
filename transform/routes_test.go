package transform

import (
	"errors"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"net/http"
	"testing"
)

func router(t *testing.T, mode string) *Router {
	t.Helper()
	r, err := NewRouter([]Route{{PathPrefix: "/images/", Mode: mode, Policy: policy()}}, capabilities())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestManagedSourceMappingPreservesUnrelatedQueryAndEscapedPath(t *testing.T) {
	r := router(t, "auto")
	for _, test := range []struct{ input, want string }{
		{"https://example.com/images/a.jpg?x=1&format=webp&x=2&quality=80&token=a%2Bb", "https://example.com/images/a.jpg?x=1&x=2&token=a%2Bb"},
		{"https://example.com/images/a%2Fb.jpg?format=png", "https://example.com/images/a%2Fb.jpg"},
		{"https://example.com/images/a?", "https://example.com/images/a?"},
		{"https://example.com/images/a?format=webp&", "https://example.com/images/a?"},
		{"https://example.com/images/a?%66ormat=jpg&x=%26quality%3D70", "https://example.com/images/a?x=%26quality%3D70"},
		{"https://example.com/images/a?size=800&crop=center", "https://example.com/images/a?size=800&crop=center"},
	} {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", test.input, nil)
		plan, matched, err := r.Resolve(req)
		if err != nil || !matched || plan.SourceURL != test.want {
			t.Fatal(test.input, plan.SourceURL, matched, err)
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a?format=jpg&quality=080", nil)
	plan, _, err := r.Resolve(req)
	if err != nil || plan.Options.Format != "image/jpeg" || *plan.Options.Quality != 80 {
		t.Fatal(plan, err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), "GET", "https://example.com/image/a?format=webp", nil)
	if _, matched, err := r.Resolve(req); err != nil || matched {
		t.Fatal("prefix boundary ignored", matched, err)
	}
	req, _ = http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images%2Fa?format=webp", nil)
	if _, matched, _ := r.Resolve(req); matched {
		t.Fatal("escaped slash became a directory separator")
	}
}
func TestManagedMappingRejectsDuplicateConflictingOrMalformedParameters(t *testing.T) {
	r := router(t, "auto")
	for _, query := range []string{"format=webp&format=png", "format=webp&%66ormat=webp", "quality=80&quality=80", "format=avif", "format=", "quality=0", "quality=101", "quality=1.0", "quality=%2B80", "quality=00080", "format=source&quality=80", "x=1;format=webp", "x=%zz"} {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a?"+query, nil)
		if _, matched, err := r.Resolve(req); !matched || !errors.Is(err, ErrInvalidParameter) {
			t.Fatal(query, matched, err)
		}
	}
	explicit := router(t, "explicit")
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/a", nil)
	if _, _, err := explicit.Resolve(req); !errors.Is(err, ErrInvalidParameter) {
		t.Fatal("missing explicit format accepted", err)
	}
}
func TestManagedRouterUsesLongestPrefixAndOwnsPolicy(t *testing.T) {
	outer, inner := policy(), policy()
	inner.PolicyVersion = "inner"
	routes := []Route{{PathPrefix: "/images/", Policy: outer}, {PathPrefix: "/images/small/", Policy: inner}}
	r, err := NewRouter(routes, capabilities())
	if err != nil {
		t.Fatal(err)
	}
	routes[1].Policy.Formats[0] = "image/png"
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/images/small/a?format=webp", nil)
	plan, _, err := r.Resolve(req)
	if err != nil || plan.Policy.PolicyVersion != "inner" || plan.Policy.Formats[0] != "image/webp" {
		t.Fatal(plan, err)
	}
	for _, prefix := range []string{"/images", "//other/images/", "https://other/images/", "/images/?x=1", "/images/#fragment"} {
		if _, err = NewRouter([]Route{{PathPrefix: prefix, Policy: policy()}}, capabilities()); err == nil {
			t.Fatal("invalid route accepted", prefix)
		}
	}
}

func TestManagedRouteDefaultsFitDeclaredBackendBounds(t *testing.T) {
	caps := capabilities()
	caps.Outputs[0].QualityMax = 50
	caps.Outputs[1].CompressionMax = 5
	caps.FlattenAlpha = false
	route := Route{PathPrefix: "/images/", Policy: transformv1.Policy{Formats: []string{"image/jpeg", "image/png"}, PolicyVersion: "p1"}}
	if _, err := NewRouter([]Route{route}, caps); err == nil {
		t.Fatal("unusable quality/compression defaults accepted")
	}
	route.Policy.Quality = 50
	level := 5
	route.Policy.PNGCompression = &level
	if _, err := NewRouter([]Route{route}, caps); err != nil {
		t.Fatal(err)
	}
	route.Policy.AlphaPolicy, route.Policy.Background = "flatten", "#ffffff"
	if _, err := NewRouter([]Route{route}, caps); err == nil {
		t.Fatal("unsupported JPEG flatten configured")
	}
}
