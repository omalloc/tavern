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
	var calls atomic.Int32
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		originImage(w, r.Header.Get("Accept"), "Accept", `"origin"`)
	})
	path, state := t.TempDir(), t.TempDir()
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

func TestRepresentationPeerOverrideIsPartOfIsolation(t *testing.T) {
	_, _, target := newVaryTestCache(t, func(http.ResponseWriter, *http.Request) {})
	c := &Caching{opt: &cachingOption{IncludeQueryInCacheKey: true, representation: &representationLookup{namespace: "v2", isolationScope: "origin"}}}
	a := varyRequest(t, target, nil)
	b := a.Clone(a.Context())
	b.Header.Set(protocol.InternalUpstreamAddr, "other-origin:80")
	ka, err := c.representationKey(a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := c.representationKey(b)
	if err != nil {
		t.Fatal(err)
	}
	if ka.ResourceID() == kb.ResourceID() {
		t.Fatal("peer override reused another origin's representation")
	}
}
