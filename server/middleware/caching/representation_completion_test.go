package caching

import (
	"context"
	"fmt"
	"io"
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
	storageimpl "github.com/omalloc/tavern/storage"
)

// A client can finish as soon as Content-Length bytes arrive. Simulate both a
// length-limited consumer and cancellation at the final downstream Write; neither
// needs to wait for a separate upstream Read returning EOF.
func TestRepresentationSharedBodyCompletion(t *testing.T) {
	for _, size := range []int{150, 1 << 20} {
		for _, vary := range []string{"", "Accept"} {
			for _, cancelOnWrite := range []bool{false, true} {
				t.Run(fmt.Sprintf("size=%d/vary=%q/cancel=%t", size, vary, cancelOnWrite), func(t *testing.T) {
					var calls atomic.Int32
					want := strings.Repeat("x", size)
					f := newOriginFixture(t, func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						originImage(w, want, vary, `"origin"`)
					})
					f.opt.SliceSize = 1 << 19
					f.opt.ResponseSharing = &responseSharingOption{Enabled: true}
					if err := f.opt.bindResponseSharing(); err != nil {
						t.Fatal(err)
					}
					header := http.Header{"Accept": {"image/jpeg"}}
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					resp, err := f.rt.RoundTrip(varyRequest(t, f.target, header).WithContext(ctx))
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "MISS") {
						t.Fatal("cold request did not miss", resp.Header)
					}
					var got strings.Builder
					if cancelOnWrite {
						sink := &cancelAfterBodyWriter{dst: &got, remaining: int64(size), cancel: cancel}
						_, err = io.CopyBuffer(sink, resp.Body, make([]byte, 32<<10))
					} else {
						_, err = io.CopyN(&got, resp.Body, int64(size))
					}
					if err != nil || got.String() != want {
						t.Fatal("response body changed", got.Len(), err)
					}
					if err := resp.Body.Close(); err != nil {
						t.Fatal(err)
					}
					assertSharingReleased(t, f.opt.sharing)
					selected, err := f.repo.Lookup(t.Context(), f.key, header)
					if err != nil || selected == nil || !selected.Representation.HasComplete() {
						t.Fatal("complete response was not published", selected, err)
					}
					id := selected.Representation.ID.Hash()
					for range 2 {
						hit, body := f.get(t, header)
						if body != want || !strings.HasPrefix(hit.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") {
							t.Fatal("completed response did not hit", hit.Header, len(body))
						}
						selected, err = f.repo.Lookup(t.Context(), f.key, header)
						if err != nil || selected.Representation.ID.Hash() != id {
							t.Fatal("cache hit replaced the stored generation", err)
						}
					}
					if calls.Load() != 1 {
						t.Fatal("repeated requests fetched the same file again", calls.Load())
					}
				})
			}
		}
	}
}

func TestPublicSharedResponseCompletionSurvivesReopen(t *testing.T) {
	var calls atomic.Int32
	want := strings.Repeat("x", 1<<20)
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		originImage(w, want, "", `"origin"`)
	})
	cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring",
		Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()},
		Buckets:        []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	open := func() (storage.Storage, http.RoundTripper) {
		t.Helper()
		store, err := storageimpl.New(cfg, log.GetLogger())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		storageimpl.SetDefault(store)
		mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{
			"slice_size": 1 << 19, "include_query_in_cache_key": true,
			"representation":   map[string]any{"enabled": true},
			"response_sharing": map[string]any{"enabled": true},
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		return store, mw(proxy.Transport{Proxy: proxy.GetProxy()})
	}
	store, rt := open()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	resp, err := rt.RoundTrip(varyRequest(t, target, nil).WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body strings.Builder
	sink := &cancelAfterBodyWriter{dst: &body, remaining: int64(len(want)), cancel: cancel}
	_, err = io.CopyBuffer(sink, resp.Body, make([]byte, 32<<10))
	closeErr := resp.Body.Close()
	if err != nil || closeErr != nil || body.String() != want {
		t.Fatal("cold response failed", body.Len(), err, closeErr)
	}
	getHit := func() {
		t.Helper()
		hit, body, err := readVaryResponse(rt, varyRequest(t, target, nil))
		if err != nil || body != want || !strings.HasPrefix(hit.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") || calls.Load() != 1 {
			t.Fatal("completed shared response did not hit", err, calls.Load())
		}
	}
	getHit()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, rt = open()
	getHit()
}

type cancelAfterBodyWriter struct {
	dst       io.Writer
	remaining int64
	cancel    context.CancelFunc
}

func (w *cancelAfterBodyWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	if w.remaining == 0 {
		w.cancel()
	}
	return n, err
}
