package caching

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/lifecycle"
	"github.com/omalloc/tavern/server/middleware"
	storagev1 "github.com/omalloc/tavern/storage"
)

type taskTestStorage struct {
	*representationTestStorage
	work       lifecycle.Group
	selections atomic.Int32
}

func (s *taskTestStorage) BeginCacheTask(ctx context.Context, timeout time.Duration) (context.Context, func(), error) {
	return s.work.Begin(ctx, timeout)
}
func (s *taskTestStorage) Drain(ctx context.Context) error { return s.work.Drain(ctx) }
func (s *taskTestStorage) Select(ctx context.Context, id *object.ID) storage.Bucket {
	s.selections.Add(1)
	return s.bucket
}

func TestInjectedOriginAndDrainingBypass(t *testing.T) {
	_, bucket, target := newVaryTestCache(t, func(http.ResponseWriter, *http.Request) { t.Error("cache bypassed the injected transport") })
	store := &taskTestStorage{representationTestStorage: &representationTestStorage{bucket: bucket}}
	storagev1.SetDefault(store)
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"slice_size": 64}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var calls atomic.Int32
	type contextKey struct{}
	origin := middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.Context().Value(contextKey{}) != "preserved" {
			t.Error("context values lost")
		}
		if _, ok := req.Context().Deadline(); !ok {
			t.Error("caller deadline lost")
		}
		if req.RequestURI != "" {
			t.Error("RequestURI not removed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"no-store"}, "Content-Length": {"4"}}, ContentLength: 4, Body: io.NopCloser(strings.NewReader("body"))}, nil
	})
	rt := mw(origin)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), contextKey{}, "preserved"), time.Minute)
	defer cancel()
	for _, method := range []string{"GET", "HEAD", "POST", "OPTIONS"} {
		req, _ := http.NewRequestWithContext(ctx, method, target, nil)
		req.RequestURI = "/image"
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if req.RequestURI != "/image" {
			t.Fatal("mutated client request")
		}
	}
	if calls.Load() != 4 {
		t.Fatal("origin calls", calls.Load())
	}
	before := store.selections.Load()
	if err = store.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 5 || store.selections.Load() != before {
		t.Fatal("draining request touched storage")
	}
}

func TestDrainCancelsBlockedFillAndJoinsBodyCallbacks(t *testing.T) {
	_, bucket, target := newVaryTestCache(t, func(http.ResponseWriter, *http.Request) { t.Error("global origin called") })
	store := &taskTestStorage{representationTestStorage: &representationTestStorage{bucket: bucket}}
	storagev1.SetDefault(store)
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"slice_size": 4, "async_flush_chunk": true}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	reader, writer := io.Pipe()
	defer writer.Close()
	rt := mw(middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": {"max-age=600"}, "Content-Length": {"16"}}, ContentLength: 16, Body: reader}, nil
	}))
	resp, err := rt.RoundTrip(varyRequest(t, target, nil))
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() { io.Copy(io.Discard, resp.Body); resp.Body.Close(); close(readDone) }()
	drainCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = store.Drain(drainCtx); err != nil {
		t.Fatal("blocked fill prevented drain", err)
	}
	select {
	case <-readDone:
	case <-drainCtx.Done():
		t.Fatal("body reader remained active after drain")
	}
}
