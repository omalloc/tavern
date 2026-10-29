package caching

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/proxy/selector"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/proxy"
	storagev1 "github.com/omalloc/tavern/storage"
	"github.com/omalloc/tavern/storage/bucket/memory"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
	"github.com/omalloc/tavern/storage/sharedkv"
)

type varyTestStorage struct {
	storage.Storage
	bucket storage.Bucket
}

func (s *varyTestStorage) Select(context.Context, *object.ID) storage.Bucket { return s.bucket }

// Exercise the production middleware and real proxy against a controlled origin.
// Globals are restored on cleanup, so these tests deliberately do not run parallel.
func newVaryTestCache(t *testing.T, handler http.HandlerFunc) (http.RoundTripper, storage.Bucket, string) {
	t.Helper()
	origin := httptest.NewServer(handler)
	t.Cleanup(origin.Close)
	kv := sharedkv.NewMemSharedKV()
	t.Cleanup(func() { _ = kv.Close() })
	bucket, err := memory.New(&storage.BucketConfig{
		Driver: storage.TypeInMemory, Type: "vary-test", MaxObjectLimit: 100,
	}, kv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bucket.Close() })
	previousProxy, previousStorage := proxy.GetProxy(), storagev1.Current()
	t.Cleanup(func() {
		proxy.SetDefault(previousProxy)
		storagev1.SetDefault(previousStorage)
	})
	proxy.SetDefault(proxy.New(proxy.WithInitialNodes([]selector.Node{
		selector.NewNode("http", origin.Listener.Addr().String(), nil),
	})))
	storagev1.SetDefault(&varyTestStorage{bucket: bucket})
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{
		"include_query_in_cache_key":     true,
		"collapsed_request":              true,
		"collapsed_request_wait_timeout": "5s",
		"slice_size":                     64,
		"async_flush_chunk":              true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return mw(nil), bucket, origin.URL + "/image"
}

func varyRequest(t *testing.T, target string, header http.Header) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	return req
}

func readVaryResponse(rt http.RoundTripper, req *http.Request) (*http.Response, string, error) {
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return resp, "", err
	}
	if resp.Body == nil {
		return resp, "", nil
	}
	body, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp, string(body), readErr
	}
	return resp, string(body), closeErr
}

func requireEmptyVaryCache(t *testing.T, bucket storage.Bucket) {
	t.Helper()
	count := 0
	if err := bucket.Iterate(t.Context(), func(*object.Metadata) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("bypass stored %d cache records", count)
	}
}

func TestVaryColdMissDoesNotCollapse(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- r.Header.Get("Accept")
		<-release
		w.Header().Set("Vary", "Accept")
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Type", r.Header.Get("Accept"))
		fmt.Fprint(w, r.Header.Get("Accept")+" bytes")
	})
	type result struct {
		mime, body string
		err        error
	}
	results := make(chan result, 2)
	for _, mime := range []string{"image/webp", "image/jpeg"} {
		req := varyRequest(t, target, http.Header{"Accept": {mime}})
		go func() {
			resp, body, err := readVaryResponse(rt, req)
			if err == nil && resp.Header.Get("Content-Type") != req.Header.Get("Accept") {
				err = fmt.Errorf("MIME = %s, want %s", resp.Header.Get("Content-Type"), req.Header.Get("Accept"))
			}
			results <- result{req.Header.Get("Accept"), body, err}
		}()
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for range 2 {
		select {
		case <-entered:
		case <-timer.C:
			unblock()
			t.Fatal("concurrent requests did not both enter origin (unsafe response collapse)")
		}
	}
	unblock()
	for range 2 {
		r := <-results
		if r.err != nil || r.body != r.mime+" bytes" {
			t.Errorf("%s response: %q, %v", r.mime, r.body, r.err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("origin calls = %d, want 2", calls.Load())
	}
	requireEmptyVaryCache(t, bucket)
}

func TestVaryBypass(t *testing.T) {
	for _, vary := range []string{"*", "Accept, *", "Accept", "Accept, Accept-Encoding", "bad field", strings.Repeat("X,", 33) + "*"} {
		t.Run(vary, func(t *testing.T) {
			var calls atomic.Int32
			rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Vary", vary)
				w.Header().Set("Cache-Control", "max-age=600")
				w.Header().Set("Content-Type", "image/jpeg")
				fmt.Fprint(w, "source image")
			})
			for range 2 {
				resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
				if err != nil {
					t.Fatal(err)
				}
				if resp.Header.Get("Vary") != vary || body != "source image" || !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
					t.Fatalf("bypass response: %v, %q", resp.Header, body)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("origin calls = %d", calls.Load())
			}
			requireEmptyVaryCache(t, bucket)
		})
	}
}

func TestCacheAdmissionResponse(t *testing.T) {
	for _, control := range []string{"max-age=600,no-store", "max-age=600,private", "max-age=600,no-cache"} {
		t.Run(control, func(t *testing.T) {
			var calls atomic.Int32
			rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", control)
				w.Header().Set(protocol.ProtocolCacheTime, "600")
				fmt.Fprint(w, "uncacheable")
			})
			for range 2 {
				_, body, err := readVaryResponse(rt, varyRequest(t, target, nil))
				if err != nil || body != "uncacheable" {
					t.Fatalf("response = %q, %v", body, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("origin calls = %d", calls.Load())
			}
			requireEmptyVaryCache(t, bucket)
		})
	}
}

func TestCacheAdmissionRequest(t *testing.T) {
	for _, header := range []http.Header{
		{"Authorization": {"Bearer secret"}},
		{"Authorization": {""}},
		{"Cache-Control": {"no-store"}},
		{"Cache-Control": {"no-cache"}},
		{"Cache-Control": {"max-age=0"}},
		{"Pragma": {"no-cache"}},
	} {
		t.Run(fmt.Sprint(header), func(t *testing.T) {
			var calls atomic.Int32
			rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", "max-age=600")
				fmt.Fprint(w, "origin")
			})
			// A fresh, readable ordinary object must not bypass request policy.
			id := object.NewID(target)
			seedVaryTestObject(t, bucket, &object.Metadata{ID: id, Code: 200, Size: 6, BlockSize: 64,
				RespUnix: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(), Headers: http.Header{"Content-Type": {"text/plain"}},
			}, "cached")
			for range 2 {
				resp, body, err := readVaryResponse(rt, varyRequest(t, target, header))
				if err != nil || body != "origin" {
					t.Fatalf("response = %q, %v", body, err)
				}
				if !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "BYPASS") {
					t.Fatal(resp.Header)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("origin calls = %d", calls.Load())
			}
			md, err := bucket.Lookup(t.Context(), id)
			if err != nil || md == nil || md.Size != 6 {
				t.Fatalf("bypass changed existing metadata: %v, %v", md, err)
			}
		})
	}
}

func seedVaryTestObject(t *testing.T, bucket storage.Bucket, md *object.Metadata, body string) {
	t.Helper()
	if body != "" {
		writer, _, err := bucket.WriteChunkFile(t.Context(), md.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(writer, body); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		md.Chunks.Set(0)
	}
	if err := bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
}

func TestVaryLegacyBypass(t *testing.T) {
	for _, flag := range []object.CacheFlag{object.FlagVaryIndex, object.FlagVaryCache, object.FlagCache} {
		t.Run(flag.String(), func(t *testing.T) {
			var calls atomic.Int32
			rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Cache-Control", "max-age=600")
				w.Header().Set("Vary", "Accept")
				fmt.Fprint(w, "correct origin image")
			})
			seedVaryTestObject(t, bucket, &object.Metadata{ID: object.NewID(target), Flags: flag,
				BlockSize: 64, Size: 5, Code: 200, ExpiresAt: time.Now().Add(time.Hour).Unix(),
				Headers: http.Header{"Vary": {"Accept"}}, VirtualKey: []string{"Accept=image/webp,image/jpeg"},
			}, "wrong")
			for range 2 {
				_, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
				if err != nil || body != "correct origin image" {
					t.Fatalf("response = %q, %v", body, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("origin calls = %d", calls.Load())
			}
		})
	}
}

func TestCacheAdmissionOrdinaryHit(t *testing.T) {
	var calls atomic.Int32
	rt, _, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Length", "12")
		fmt.Fprint(w, "normal image")
	})
	for i := range 2 {
		resp, body, err := readVaryResponse(rt, varyRequest(t, target, nil))
		if err != nil || body != "normal image" {
			t.Fatalf("response = %q, %v", body, err)
		}
		if i == 1 && !strings.HasPrefix(resp.Header.Get(protocol.ProtocolCacheStatusKey), "HIT") {
			t.Fatal(resp.Header)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("ordinary hit made %d origin calls", calls.Load())
	}
}

func TestVaryResponseReplacesOrdinaryCache(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotModified} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Vary", "Accept")
				w.Header().Set("Cache-Control", "max-age=600")
				w.Header().Set("Age", "120")
				if n == 1 && status == http.StatusNotModified {
					if r.Header.Get("If-None-Match") != `"old"` {
						t.Error("missing cache validator")
					}
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if n == 2 && status == http.StatusNotModified && r.Header.Get("If-None-Match") != "" {
					t.Error("unconditional retry retained cache validator")
				}
				w.Header().Set("Content-Type", "image/jpeg")
				fmt.Fprint(w, "fresh jpeg")
			})
			seedVaryTestObject(t, bucket, &object.Metadata{
				ID: object.NewID(target), Code: 200, Size: 5, BlockSize: 64,
				RespUnix: time.Now().Add(-time.Hour).Unix(), ExpiresAt: time.Now().Add(-time.Minute).Unix(),
				Headers: http.Header{"Etag": {`"old"`}, "Cache-Control": {"max-age=600"}},
			}, "stale")
			resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
			if err != nil || resp.StatusCode != 200 || body != "fresh jpeg" {
				t.Fatalf("replacement response = %v, %q, %v", resp, body, err)
			}
			if resp.Header.Get("Age") != "120" || resp.Header.Get("Vary") != "Accept" {
				t.Fatalf("bypass changed origin metadata: %v", resp.Header)
			}
			wantCalls := int32(1)
			if status == http.StatusNotModified {
				wantCalls++
			}
			if calls.Load() != wantCalls {
				t.Fatalf("origin calls = %d, want %d", calls.Load(), wantCalls)
			}
			requireEmptyVaryCache(t, bucket)
		})
	}
}

func TestVaryRangeBypassTrimsExpandedResponse(t *testing.T) {
	rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-63" {
			t.Errorf("origin Range = %s", r.Header.Get("Range"))
		}
		w.Header().Set("Vary", "Accept")
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", "bytes 0-63/128")
		w.Header().Set("Content-Length", "64")
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, strings.Repeat("01234567", 8))
	})
	resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{
		"Range": {"bytes=16-23"}, "Accept": {"image/jpeg"},
	}))
	if err != nil || body != "01234567" {
		t.Fatalf("range response = %q, %v", body, err)
	}
	if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != "bytes 16-23/128" ||
		resp.ContentLength != 8 || resp.Header.Get("Content-Length") != "8" {
		t.Fatalf("incorrect final range metadata: %v, length %d", resp.Header, resp.ContentLength)
	}
	requireEmptyVaryCache(t, bucket)
}

func TestCacheAdmissionLegacyPolicy(t *testing.T) {
	for _, control := range []string{"max-age=600,no-store", "max-age=600,private", "max-age=600,no-cache"} {
		t.Run(control, func(t *testing.T) {
			rt, bucket, target := newVaryTestCache(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", control)
				fmt.Fprint(w, "current origin")
			})
			seedVaryTestObject(t, bucket, &object.Metadata{
				ID: object.NewID(target), Code: 200, Size: 7, BlockSize: 64,
				RespUnix: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
				Headers: http.Header{"Cache-Control": {control}},
			}, "private")
			_, body, err := readVaryResponse(rt, varyRequest(t, target, nil))
			if err != nil || body != "current origin" {
				t.Fatalf("response = %q, %v", body, err)
			}
			requireEmptyVaryCache(t, bucket)
		})
	}
}
