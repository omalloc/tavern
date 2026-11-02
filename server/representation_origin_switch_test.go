package server

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/server/middleware"
	"github.com/omalloc/tavern/storage"
)

func TestPublicRepresentationUpstreamChangeRetainsCacheAfterReopen(t *testing.T) {
	previous := storage.Current()
	defer storage.SetDefault(previous)
	cfg := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", SliceSize: 64,
		DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()},
		Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}
	var primaryCalls, backupCalls int
	open := func(upstream *conf.Upstream, body string, calls *int) (storagev1.Storage, http.RoundTripper, func()) {
		t.Helper()
		store, err := storage.New(cfg, log.DefaultLogger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		storage.SetDefault(store)
		server := &HTTPServer{config: &conf.Bootstrap{Storage: cfg, Upstream: upstream, Server: &conf.Server{
			Middleware: []*middlewarev1.Middleware{{Name: "caching", Options: map[string]any{
				"include_query_in_cache_key": true, "representation": map[string]any{"enabled": true},
			}}},
		}}}
		origin := middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			*calls++
			return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=600"}, "Content-Length": {strconv.Itoa(len(body))}},
				ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})
		rt, err := server.buildMiddlewareChain(origin)
		if err != nil {
			t.Fatal(err)
		}
		cleanup := func() {
			for _, close := range server.cleanups {
				close()
			}
		}
		return store, rt, cleanup
	}
	get := func(rt http.RoundTripper, body, status string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://cdn.example/image", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != 200 || string(data) != body || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), status) {
			t.Fatalf("status=%d cache=%s body=%q err=%v", resp.StatusCode, resp.Header.Get(protocol.ProtocolCacheStatusKey), data, err)
		}
	}
	store, rt, cleanup := open(&conf.Upstream{Address: []string{"http://primary:8000"}, Balancing: "wrr"}, "primary bytes", &primaryCalls)
	get(rt, "primary bytes", "MISS")
	cleanup()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, rt, cleanup = open(&conf.Upstream{Address: []string{"http://backup:8000", "http://other:8000"}, Balancing: "random", MaxIdleConns: 100}, "backup bytes", &backupCalls)
	defer cleanup()
	get(rt, "primary bytes", "HIT")
	if primaryCalls != 1 || backupCalls != 0 {
		t.Fatal("upstream configuration change discarded persisted cache", primaryCalls, backupCalls)
	}
	if err := store.PURGE("https://cdn.example/image", storagev1.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	get(rt, "backup bytes", "MISS")
	get(rt, "backup bytes", "HIT")
	if backupCalls != 1 {
		t.Fatal("new origin was not cached", backupCalls)
	}
}
