package caching

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/proxy"
	storagev1 "github.com/omalloc/tavern/storage"
)

func TestPublicRepresentationReopenRollbackAndPurge(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		t.Run(map[bool]string{true: "explicit-state", false: "default-state"}[explicit], func(t *testing.T) { testPublicRepresentationReopenRollbackAndPurge(t, explicit) })
	}
}

func testPublicRepresentationReopenRollbackAndPurge(t *testing.T, explicit bool) {
	var calls atomic.Int32
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		originImage(w, r.Header.Get("Accept"), "Accept", `"origin"`)
	})
	path, state := t.TempDir(), ""
	if explicit {
		state = t.TempDir()
	}
	config := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: state}, Buckets: []*conf.Bucket{{Path: path, MaxObjectLimit: 100}}}
	open := func(enabled bool) (storage.Storage, http.RoundTripper) {
		t.Helper()
		store, err := storagev1.New(config, log.GetLogger())
		if err != nil {
			t.Fatal(err)
		}
		storagev1.SetDefault(store)
		t.Cleanup(func() { store.Close() })
		mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"slice_size": 64, "include_query_in_cache_key": true, "representation": map[string]any{"enabled": enabled, "namespace": "http-v2"}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		return store, mw(proxy.Transport{Proxy: proxy.GetProxy()})
	}
	get := func(rt http.RoundTripper, mime string, hit bool) {
		t.Helper()
		resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {mime}}))
		if err != nil || body != mime {
			t.Fatal(body, err)
		}
		status := resp.Header.Get(protocol.ProtocolCacheStatusKey)
		if strings.HasPrefix(status, "HIT") != hit {
			t.Fatal("unexpected cache status", status)
		}
	}
	store, rt := open(true)
	for _, mime := range []string{"image/jpeg", "image/webp"} {
		get(rt, mime, false)
		get(rt, mime, true)
	}
	if calls.Load() != 2 {
		t.Fatal("variants mixed", calls.Load())
	}
	store.Close()
	store, rt = open(true)
	get(rt, "image/jpeg", true)
	get(rt, "image/webp", true)
	if calls.Load() != 2 {
		t.Fatal("restart lost published variants")
	}
	store.Close()
	store, rt = open(false)
	get(rt, "image/jpeg", false)
	get(rt, "image/jpeg", false)
	if err := store.PURGE(target, storage.PurgeControl{Hard: true}); err != nil {
		t.Fatal("disabled v2 lost purge ownership", err)
	}
	store.Close()
	store, rt = open(true)
	get(rt, "image/jpeg", false)
	get(rt, "image/jpeg", true)
	if calls.Load() != 5 {
		t.Fatal("rollback purge failed", fmt.Sprint(calls.Load()))
	}
	store.Close()
	// Removing the binding would silently drop all rollback-time barriers.
	config.Representation = nil
	if unsafe, err := storagev1.New(config, log.GetLogger()); err == nil {
		unsafe.Close()
		t.Fatal("removed persisted control ownership")
	}
}

func TestRepresentationKeyUsesPublicIdentity(t *testing.T) {
	c := &Caching{opt: &cachingOption{IncludeQueryInCacheKey: true, representation: &representationLookup{namespace: "v2", isolationScope: "cdn"}}}
	original, err := http.NewRequest(http.MethodGet, "http://origin-a:8000/image?q=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	original.Host = "cdn.example"
	key, err := c.representationKey(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		same   bool
	}{
		{"upstream header", func(r *http.Request) { r.Header.Set(protocol.InternalUpstreamAddr, "origin-b:8000") }, true},
		{"dial target", func(r *http.Request) { r.URL.Host = "origin-b:8000" }, true},
		{"public host", func(r *http.Request) { r.Host = "other.example" }, false},
		{"path", func(r *http.Request) { r.URL.Path = "/other" }, false},
		{"query", func(r *http.Request) { r.URL.RawQuery = "q=2" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := original.Clone(original.Context())
			tc.change(req)
			got, err := c.representationKey(req)
			if err != nil {
				t.Fatal(err)
			}
			if (got.ResourceID() == key.ResourceID()) != tc.same {
				t.Fatal("unexpected resource identity", got.CanonicalURL())
			}
		})
	}
	for _, tc := range []struct{ name, namespace, scope string }{
		{"namespace", "other", "cdn"}, {"explicit scope", "v2", "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolated := &Caching{opt: &cachingOption{IncludeQueryInCacheKey: true, representation: &representationLookup{namespace: tc.namespace, isolationScope: tc.scope}}}
			got, err := isolated.representationKey(original)
			if err != nil {
				t.Fatal(err)
			}
			if got.ResourceID() == key.ResourceID() {
				t.Fatal("explicit isolation lost")
			}
		})
	}
}
