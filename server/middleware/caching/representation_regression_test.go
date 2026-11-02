package caching

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/proxy/selector"
	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/proxy"
	storageimpl "github.com/omalloc/tavern/storage"
	"github.com/omalloc/tavern/storage/representation"
	planning "github.com/omalloc/tavern/transform"
)

func TestRepresentationOriginSwitchRetainsCache(t *testing.T) {
	for _, mode := range []string{"header", "context", "balancer"} {
		t.Run(mode, func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				originImage(w, "origin A", "", `"a"`)
			})
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				w.Header().Set("Date", time.Unix(f.clock.Load(), 0).UTC().Format(http.TimeFormat))
				originImage(w, "origin B", "", `"b"`)
			}))
			defer other.Close()
			first, body := f.get(t, nil)
			if first.StatusCode != 200 || body != "origin A" {
				t.Fatal(body)
			}
			originalID := f.root(t).Resource.Variants[0].RepresentationID.Hash()
			target, err := url.Parse(other.URL)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "balancer" {
				proxy.GetProxy().Apply([]selector.Node{selector.NewNode("http", target.Host, nil)})
			}
			request := func(wantBody, wantStatus string) {
				t.Helper()
				req := varyRequest(t, f.target, nil)
				switch mode {
				case "header":
					req.Header.Set(protocol.InternalUpstreamAddr, target.Host)
				case "context":
					req = req.WithContext(selector.NewPeerContext(req.Context(), selector.NewPeer(selector.NewNode("http", target.Host, nil))))
				}
				resp, body, err := readVaryResponse(f.rt, req)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 || body != wantBody || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), wantStatus) {
					t.Fatalf("origin switch: status=%d cache=%s body=%q", resp.StatusCode, resp.Header.Get(protocol.ProtocolCacheStatusKey), body)
				}
			}
			for range 2 {
				request("origin A", "HIT")
			}
			if backupCalls.Load() != 0 || f.root(t).Resource.Variants[0].RepresentationID.Hash() != originalID {
				t.Fatal("origin switch discarded a fresh representation", backupCalls.Load())
			}
			f.clock.Add(601)
			request("origin B", "REVALIDATE_MISS")
			request("origin B", "HIT")
			if _, body := f.get(t, nil); body != "origin B" {
				t.Fatal("replacement not shared with default routing", body)
			}
			if primaryCalls.Load() != 1 || backupCalls.Load() != 1 {
				t.Fatal("unexpected origin calls", primaryCalls.Load(), backupCalls.Load())
			}
		})
	}
}

func TestRepresentationVaryReplacementAtBudget(t *testing.T) {
	for _, status := range []int{200, 304} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var phase atomic.Bool
			var calls atomic.Int32
			f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if phase.Load() {
					w.Header().Set("Vary", "X-Theme")
					if status == 304 {
						w.WriteHeader(304)
						return
					}
					originImage(w, "image", "X-Theme", `"v1"`)
					return
				}
				originImage(w, "image", "Accept", `"v1"`)
			})
			f.opt.VaryLimit = 1
			h := http.Header{"Accept": {"image/jpeg"}, "X-Theme": {"dark"}}
			f.get(t, h)
			f.clock.Add(601)
			phase.Store(true)
			for range 2 {
				if _, body := f.get(t, h); body != "image" {
					t.Fatal(body)
				}
			}
			if calls.Load() != 2 || len(f.root(t).Resource.Variants) != 1 {
				t.Fatal("replacement failed to reuse the retired slot", calls.Load())
			}
		})
	}
}

func TestManagedExplicitRoutePurge(t *testing.T) {
	for _, raw := range []string{"https://example.com/images/", "https://example.com/images/a.jpg", "https://example.com/images/a.jpg?format=png"} {
		t.Run(raw, func(t *testing.T) {
			f := newManagedFixture(t)
			router, err := planning.NewRouter([]planning.Route{{PathPrefix: "/images/", Mode: "explicit", Policy: transformv1.Policy{Formats: []string{"image/png", "image/jpeg"}, PolicyVersion: "route-v1"}}}, f.backend.Capabilities())
			if err != nil {
				t.Fatal(err)
			}
			f.router = router
			f.store.purgers = map[string]storage.RepresentationPurger{"explicit": &managedPurger{manager: f.store.manager, router: router}}
			f.get(t, "format=png", "image/png")
			if err := f.store.PURGE(raw, storage.PurgeControl{Hard: true, Dir: raw == "https://example.com/images/"}); err != nil {
				t.Fatal(err)
			}
			f.get(t, "format=png", "image/png")
			if f.origins.Load() != 2 || f.backend.calls.Load() != 2 {
				t.Fatal("explicit route purge retained source/product", f.origins.Load(), f.backend.calls.Load())
			}
		})
	}
}

func TestManagedExpiredSource304ReusesProduct(t *testing.T) {
	f := newManagedFixture(t)
	first, _ := f.get(t, "format=png", "image/png")
	req, plan := f.request(t, "format=png", "image/png")
	sourceReq, err := f.provider.sourceRequest(req, plan)
	if err != nil {
		t.Fatal(err)
	}
	c := &Caching{opt: &f.provider.opt}
	key, err := c.representationKey(sourceReq)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := f.store.manager.Repository(key)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := repo.LookupOriginMetadata(t.Context(), key, representationSelectionHeader(sourceReq))
	if err != nil {
		t.Fatal(err)
	}
	// Expire the source and give its previous age a full lifetime without
	// sleeping or changing the immutable body/reference identity.
	md := selected.Representation.Clone()
	md.RespUnix = repo.Now().Unix() - 601
	md.ExpiresAt = repo.Now().Unix() - 1
	if err := f.store.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.status304 = true
	f.mu.Unlock()
	for range 2 {
		result, _ := f.get(t, "format=png", "image/png")
		if result.Metadata.ID.Hash() != first.Metadata.ID.Hash() {
			t.Fatal("source validation replaced immutable product")
		}
	}
	if f.origins.Load() != 2 || f.backend.calls.Load() != 1 {
		t.Fatal("expired source validation did not renew product reuse", f.origins.Load(), f.backend.calls.Load())
	}
}

func TestPublicRepresentationQuerylessPurgeSurvivesRollback(t *testing.T) {
	var calls atomic.Int32
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); originImage(w, "image", "Accept", `"v1"`) })
	cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	open := func(enabled bool, includeQuery bool) (storage.Storage, http.RoundTripper) {
		t.Helper()
		store, err := storageimpl.New(cfg, log.GetLogger())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		storageimpl.SetDefault(store)
		mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"slice_size": 64, "include_query_in_cache_key": includeQuery, "representation": map[string]any{"enabled": enabled}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		return store, mw(proxy.Transport{Proxy: proxy.GetProxy()})
	}
	get := func(rt http.RoundTripper, raw string) {
		t.Helper()
		_, body, err := readVaryResponse(rt, varyRequest(t, raw, http.Header{"Accept": {"image/jpeg"}}))
		if err != nil || body != "image" {
			t.Fatal(body, err)
		}
	}
	store, rt := open(true, false)
	get(rt, target+"?tenant=one")
	get(rt, target+"?tenant=two")
	if calls.Load() != 1 {
		t.Fatal("queryless lookup did not reuse the resource")
	}
	if err := store.PURGE(target+"?tenant=one", storage.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	get(rt, target+"?tenant=two")
	if calls.Load() != 2 {
		t.Fatal("successful queryless PURGE retained a fresh hit")
	}
	store.Close()
	store, _ = open(false, true)
	if err := store.PURGE(target+"?tenant=two", storage.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, rt = open(true, false)
	get(rt, target+"?tenant=three")
	if calls.Load() != 3 {
		t.Fatal("disabled reads lost the persisted queryless purge mapping")
	}
	store.Close()
}

func TestPublicRepresentationRejectsChangedIndexPath(t *testing.T) {
	var calls atomic.Int32
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); originImage(w, "image", "Accept", `"v1"`) })
	cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index-a", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	store, err := storageimpl.New(cfg, log.GetLogger())
	if err != nil {
		t.Fatal(err)
	}
	original := store
	t.Cleanup(func() { original.Close() })
	storageimpl.SetDefault(store)
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"slice_size": 64, "include_query_in_cache_key": true, "representation": map[string]any{"enabled": true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	_, _, err = readVaryResponse(mw(proxy.Transport{Proxy: proxy.GetProxy()}), varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	cfg.DBPath = ".index-b"
	unsafe, err := storageimpl.New(cfg, log.GetLogger())
	if unsafe != nil {
		unsafe.Close()
	}
	if !errors.Is(err, representation.ErrTopologyMismatch) {
		t.Fatal("empty replacement index accepted existing state binding", err)
	}
	cfg.DBPath = ".index-a"
	store, err = storageimpl.New(cfg, log.GetLogger())
	if err != nil {
		t.Fatal("original index could not reopen", err)
	}
	store.Close()
}
