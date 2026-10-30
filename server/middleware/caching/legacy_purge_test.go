package caching

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/omalloc/proxy/selector"
	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/storage"
)

func TestLegacyPurgeDuringBodyDoesNotResurrectOrDamageNewFill(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint("unknown=", unknown), func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Cache-Control", "max-age=600")
				body := strings.Repeat("new-", 32)
				if n == 1 {
					body = strings.Repeat("old-", 32)
				}
				if !unknown {
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				}
				w.(http.Flusher).Flush()
				if n == 1 {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				fmt.Fprint(w, body)
			}))
			t.Cleanup(origin.Close)
			previousProxy, previousStore := proxy.GetProxy(), storage.Current()
			t.Cleanup(func() { proxy.SetDefault(previousProxy); storage.SetDefault(previousStore) })
			proxy.SetDefault(proxy.New(proxy.WithInitialNodes([]selector.Node{selector.NewNode("http", origin.Listener.Addr().String(), nil)})))
			base := t.TempDir()
			store, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Buckets: []*conf.Bucket{{Path: filepath.Join(base, "cache"), Type: storagev1.TypeWarm, MaxObjectLimit: 100}}}, log.DefaultLogger)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			storage.SetDefault(store)
			mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"include_query_in_cache_key": true, "slice_size": 64, "async_flush_chunk": true}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			rt := mw(proxy.Transport{Proxy: proxy.GetProxy()})
			target := origin.URL + "/file"
			old, err := rt.RoundTrip(varyRequest(t, target, nil))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { old.Body.Close() })
			if err := store.PURGE(target, storagev1.PurgeControl{Hard: true}); err != nil {
				t.Fatal("cold in-flight purge failed", err)
			}
			if _, body, err := readVaryResponse(rt, varyRequest(t, target, nil)); err != nil || body != strings.Repeat("new-", 32) {
				t.Fatal(body, err)
			}
			unblock()
			body, err := io.ReadAll(old.Body)
			closeErr := old.Body.Close()
			if err != nil || closeErr != nil || string(body) != strings.Repeat("old-", 32) {
				t.Fatal("purge damaged old client's source response", string(body), err, closeErr)
			}
			hit, bodyString, err := readVaryResponse(rt, varyRequest(t, target, nil))
			if err != nil || bodyString != strings.Repeat("new-", 32) || !strings.HasPrefix(hit.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") || calls.Load() != 2 {
				t.Fatal("late writer replaced or deleted new fill", bodyString, err, calls.Load())
			}
			md, err := store.Buckets()[0].Lookup(t.Context(), object.NewID(target))
			if err != nil || !md.HasComplete() {
				t.Fatal(md, err)
			}
		})
	}
}
