package transform

import (
	"context"
	"net/http"
	"testing"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

func TestPlanSnapshotsCannotBeForgedOrMutatedByHeaders(t *testing.T) {
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/img/a", nil)
	req.Header.Set("Accept", "image/webp")
	req.Header.Set("X-Tavern-Transform-Spec", `{"output_mime":"image/png"}`)
	req.Header.Set("X-Tavern-Output-Mime", "image/png")
	if _, ok := PlanFromContext(req.Context()); ok {
		t.Fatal("headers became a trusted plan")
	}
	quality := 80
	p := Plan{SourceURL: req.URL.String(), Policy: policy(), Options: transformv1.RequestOptions{Format: "webp", Quality: &quality}, AcceptHeader: ClientHeaders(req)}
	ctx := WithPlan(req.Context(), p)
	quality = 1
	p.Policy.Formats[0] = "image/png"
	req.Header.Set("Accept", "image/jpeg")
	first, ok := PlanFromContext(ctx)
	if !ok || first.Options.Quality == nil || *first.Options.Quality != 80 || first.AcceptHeader.Get("Accept") != "image/webp" || first.Policy.Formats[0] != "image/webp" {
		t.Fatal(first)
	}
	if first.AcceptHeader.Get("X-Tavern-Transform-Spec") != "" {
		t.Fatal("client pseudo-internal header trusted")
	}
	first.AcceptHeader.Set("Accept", "image/png")
	*first.Options.Quality = 2
	second, _ := PlanFromContext(ctx)
	if second.AcceptHeader.Get("Accept") != "image/webp" || *second.Options.Quality != 80 {
		t.Fatal("reader mutated plan")
	}
	if _, ok = PlanFromContext(WithoutPlan(ctx)); ok {
		t.Fatal("source recursion retained target plan")
	}
	var key struct{}
	ctx = WithPlan(context.WithValue(t.Context(), key, "context"), p)
	if ctx.Value(key) != "context" {
		t.Fatal("request context lost")
	}
}
