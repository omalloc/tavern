package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/storage"
	"github.com/omalloc/tavern/storage/representation"
)

func openMemoryRepresentationCache(t *testing.T, state string) (storagev1.Storage, http.RoundTripper) {
	t.Helper()
	store, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: state}, Buckets: []*conf.Bucket{{Path: "inmemory", Driver: "memory", MaxObjectLimit: 100}}}, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	old := storage.Current()
	storage.SetDefault(store)
	t.Cleanup(func() { storage.SetDefault(old) })
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"slice_size": 64, "include_query_in_cache_key": true, "representation": map[string]any{"enabled": true, "namespace": "memory-v2"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return store, mw(proxy.Transport{Proxy: proxy.GetProxy()})
}

func TestPublicMemoryRepresentationVaryPurgeReadersAndRestart(t *testing.T) {
	var calls atomic.Int32
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		tag := fmt.Sprintf(`"%s-v1"`, r.Header.Get("Accept"))
		if r.Header.Get("If-None-Match") == tag {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "max-age=600")
			w.WriteHeader(304)
			return
		}
		body := r.Header.Get("Accept") + ".body"
		w.Header().Set("Content-Type", r.Header.Get("Accept"))
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Vary", "Accept")
		w.Header().Set("ETag", tag)
		fmt.Fprint(w, body)
	})
	state := filepath.Join(t.TempDir(), "unused-control")
	store, rt := openMemoryRepresentationCache(t, state)
	get := func(mime string, hit bool) {
		t.Helper()
		resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {mime}}))
		if err != nil || body != mime+".body" || resp.Header.Get("Content-Type") != mime || strings.HasPrefix(resp.Header.Get("X-Cache"), "HIT") != hit {
			t.Fatal("wrong memory representation", body, resp.Header, err)
		}
	}
	for _, mime := range []string{"image/jpeg", "image/webp"} {
		get(mime, false)
		get(mime, true)
	}
	if calls.Load() != 2 {
		t.Fatal("memory Vary choices did not hit independently", calls.Load())
	}
	req := varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}})
	req.Method = http.MethodHead
	resp, body, err := readVaryResponse(rt, req)
	if err != nil || body != "" || resp.ContentLength != int64(len("image/jpeg.body")) {
		t.Fatal("memory HEAD", body, err)
	}
	resp, body, err = readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}, "Range": {"bytes=1-3"}}))
	if err != nil || resp.StatusCode != 206 || body != "mag" || calls.Load() != 2 {
		t.Fatal("memory Range", body, err)
	}
	if err := store.PURGE(target, storagev1.PurgeControl{MarkExpired: true}); err != nil {
		t.Fatal(err)
	}
	// The first request validates, the next one is a fresh cache hit.
	_, _, err = readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
	if err != nil {
		t.Fatal(err)
	}
	get("image/jpeg", true)
	if calls.Load() != 3 {
		t.Fatal("memory soft PURGE did not renew through 304", calls.Load())
	}
	held, err := rt.RoundTrip(varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Body.Close()
	directory, _ := url.Parse(target)
	directory.Path = "/"
	if err := store.PURGE(directory.String(), storagev1.PurgeControl{Hard: true, Dir: true}); err != nil {
		t.Fatal(err)
	}
	manager := store.(representation.Provider).RepresentationManager()
	if _, err := manager.CollectUnreachable(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(held.Body)
	held.Body.Close()
	if err != nil || string(bytes) != "image/jpeg.body" {
		t.Fatal("memory collection deleted a pinned reader", err)
	}
	get("image/jpeg", false)
	if calls.Load() != 4 {
		t.Fatal("directory PURGE left a fresh memory hit", calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, rt = openMemoryRepresentationCache(t, state)
	key, _ := object.NewResourceKey(target, "memory-v2", "origin")
	repo, err := store.(representation.Provider).RepresentationManager().Repository(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Load(t.Context(), key); !errors.Is(err, storagev1.ErrKeyNotFound) {
		t.Fatal("new memory instance inherited cached metadata", err)
	}
	get("image/jpeg", false)
	if calls.Load() != 5 {
		t.Fatal("memory restart did not fetch fresh bytes", calls.Load())
	}
	store.Close()
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("memory mode created disk control state", err)
	}
}

func TestPublicMemoryRepresentationPurgeFencesInFlightFill(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			originImage(w, "old body", "Accept", `"old"`)
			return
		}
		originImage(w, "new body", "Accept", `"new"`)
	})
	store, rt := openMemoryRepresentationCache(t, "")
	done := make(chan error, 1)
	go func() {
		_, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
		if err == nil && body != "old body" {
			err = fmt.Errorf("old request body %q", body)
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fill did not reach origin")
	}
	if err := store.PURGE(target, storagev1.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	key, _ := object.NewResourceKey(target, "memory-v2", "origin")
	repo, _ := store.(representation.Provider).RepresentationManager().Repository(key)
	if _, err := repo.Lookup(t.Context(), key, http.Header{"Accept": {"image/jpeg"}}); !errors.Is(err, storagev1.ErrKeyNotFound) {
		t.Fatal("late fill crossed memory PURGE", err)
	}
	for range 2 {
		_, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
		if err != nil || body != "new body" {
			t.Fatal(body, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("post-PURGE memory fill did not cache", calls.Load())
	}
}

func TestPublicMemoryRepresentationDrainCancelsInFlightFill(t *testing.T) {
	entered, originClosed := make(chan struct{}), make(chan struct{})
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(originClosed)
	})
	store, rt := openMemoryRepresentationCache(t, "")
	done := make(chan error, 1)
	go func() {
		_, _, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("memory fill did not reach origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := store.(storagev1.TaskCoordinator).Drain(ctx); err != nil {
		t.Fatal("memory drain did not join the accepted fill", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("drain accepted an unfinished response")
		}
	case <-ctx.Done():
		t.Fatal("request outlived memory drain")
	}
	select {
	case <-originClosed:
	case <-ctx.Done():
		t.Fatal("origin outlived memory drain")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
