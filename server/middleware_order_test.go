package server

import (
	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/conf"
	"testing"
)

func TestRequestRewriteMustPrecedeCacheLookup(t *testing.T) {
	cache := &middlewarev1.Middleware{Name: "caching"}
	rewrite := &middlewarev1.Middleware{Name: "rewrite", Options: map[string]any{"request_headers_rewrite": map[string]any{"set": map[string]string{"Accept": "image/jpeg"}}}}
	if err := validateCacheRewriteOrder([]*middlewarev1.Middleware{cache, rewrite}); err == nil {
		t.Fatal("miss-only selection rewrite accepted")
	}
	if err := validateCacheRewriteOrder([]*middlewarev1.Middleware{rewrite, cache}); err != nil {
		t.Fatal(err)
	}
	if err := validateCacheRewriteOrder([]*middlewarev1.Middleware{cache, {Name: "rewrite", Options: map[string]any{"response_headers_rewrite": map[string]any{}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidRepresentationStartupCannotSilentlyRemoveCaching(t *testing.T) {
	server := &HTTPServer{config: &conf.Bootstrap{Storage: &conf.Storage{SliceSize: 64}, Server: &conf.Server{Middleware: []*middlewarev1.Middleware{{Name: "caching", Options: map[string]any{"representation": map[string]any{"enabled": true}}}}}}}
	if _, err := server.buildMiddlewareChain(nil); err == nil {
		t.Fatal("invalid v2 cache configuration silently became a proxy")
	}
}
