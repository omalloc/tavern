package caching

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/server/middleware"
	"github.com/omalloc/tavern/storage"
	"github.com/prometheus/client_golang/prometheus"
)

func TestResponseSharingTransparentRegressionEnabledAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, mode := range []string{"equal", "vary", "wildcard", "no-store", "conditions", "range"} {
			t.Run(fmt.Sprintf("enabled=%t/%s", enabled, mode), func(t *testing.T) {
				const count = 4
				var calls atomic.Int32
				entered := make(chan struct{}, count)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					entered <- struct{}{}
					<-release
					body := r.Header.Get("Accept") + " bytes"
					w.Header().Set("Cache-Control", "max-age=600")
					w.Header().Set("Vary", "Accept")
					w.Header().Set("Content-Type", r.Header.Get("Accept"))
					if mode == "wildcard" {
						w.Header().Set("Vary", "*")
					}
					if mode == "no-store" {
						w.Header().Set("Cache-Control", "max-age=600,no-store")
					}
					if mode == "range" {
						body = body[:3]
						w.Header().Set("Content-Range", "bytes 0-2/15")
						w.Header().Set("Content-Length", "3")
						w.WriteHeader(206)
					} else {
						w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					}
					io.WriteString(w, body)
				})
				f.opt.ResponseSharing = &responseSharingOption{Enabled: enabled}
				if err := f.opt.bindResponseSharing(); err != nil {
					t.Fatal(err)
				}
				type result struct {
					resp *http.Response
					body string
					err  error
					want string
				}
				results := make(chan result, count)
				for i := 0; i < count; i++ {
					mime := "image/jpeg"
					if mode == "vary" && i%2 == 1 {
						mime = "image/webp"
					}
					header := http.Header{"Accept": {mime}}
					if mode == "conditions" {
						header.Set("If-None-Match", `"client"`)
					}
					if mode == "range" {
						header.Set("Range", "bytes=0-2")
					}
					req := varyRequest(t, f.target, header)
					go func() { resp, body, err := readVaryResponse(f.rt, req); results <- result{resp, body, err, mime} }()
				}
				if enabled && (mode == "equal" || mode == "wildcard" || mode == "no-store") {
					waitSharingUsers(t, f.opt.sharing, count)
				} else if enabled && mode == "vary" {
					// Two independent exact snapshots, each with two callers.
					waitSharingUsers(t, f.opt.sharing, count)
				} else {
					for i := 0; i < count; i++ {
						select {
						case <-entered:
						case <-time.After(3 * time.Second):
							t.Fatal("requests did not independently reach origin")
						}
					}
				}
				unblock()
				for i := 0; i < count; i++ {
					v := <-results
					want := v.want + " bytes"
					status := 200
					if mode == "range" {
						want, status = want[:3], 206
					}
					if v.err != nil || v.resp.StatusCode != status || v.resp.Header.Get("Content-Type") != v.want || v.body != want {
						t.Fatalf("crossed response: status=%v body=%q expected=%q err=%v", v.resp, v.body, want, v.err)
					}
				}
				wantCalls := int32(count)
				if enabled && mode == "equal" {
					wantCalls = 1
				}
				if enabled && mode == "vary" {
					wantCalls = 2
				}
				if calls.Load() != wantCalls {
					t.Fatal("unexpected source calls", calls.Load(), wantCalls)
				}
				if enabled {
					assertSharingReleased(t, f.opt.sharing)
				}
			})
		}
	}
}

func TestResponseSharingPurgeDoesNotReusePreBarrierResponse(t *testing.T) {
	for _, dir := range []bool{false, true} {
		for _, hard := range []bool{false, true} {
			t.Run(fmt.Sprintf("dir=%t/hard=%t", dir, hard), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				var calls atomic.Int32
				f, store := registeredOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
					body := "new"
					if calls.Add(1) == 1 {
						close(entered)
						<-release
						body = "old"
					}
					originImage(w, body, "Accept", `"`+body+`"`)
				})
				f.opt.ResponseSharing = &responseSharingOption{Enabled: true}
				if err := f.opt.bindResponseSharing(); err != nil {
					t.Fatal(err)
				}
				oldDone := make(chan error, 1)
				go func() {
					_, body, err := readVaryResponse(f.rt, varyRequest(t, f.target, nil))
					if err == nil && body != "old" {
						err = fmt.Errorf("old reader got %q", body)
					}
					oldDone <- err
				}()
				<-entered
				target := f.target
				if dir {
					parsed, _ := url.Parse(target)
					target = parsed.Scheme + "://" + parsed.Host + "/"
				}
				if err := store.PURGE(target, storagev1.PurgeControl{Hard: hard, MarkExpired: !hard, Dir: dir}); err != nil {
					t.Fatal(err)
				}
				_, body := f.get(t, nil)
				if body != "new" || calls.Load() != 2 {
					t.Fatal("post-PURGE request joined old flight", body, calls.Load())
				}
				unblock()
				if err := <-oldDone; err != nil {
					t.Fatal(err)
				}
				_, body = f.get(t, nil)
				if body != "new" || calls.Load() != 2 {
					t.Fatal("late old flight resurrected old cache", body, calls.Load())
				}
				assertSharingReleased(t, f.opt.sharing)
			})
		}
	}
}

func TestPublicResponseSharingConfiguration(t *testing.T) {
	_, _, _ = newVaryTestCache(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected source") })
	store, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}, log.GetLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	storage.SetDefault(store)
	for _, tc := range []struct {
		name    string
		enabled bool
		sharing map[string]any
		valid   bool
	}{
		{"default-off", false, nil, true},
		{"enabled", true, map[string]any{"enabled": true}, true},
		{"requires-v2", false, map[string]any{"enabled": true}, false},
		{"negative-size", true, map[string]any{"enabled": true, "max_body_bytes": -1}, false},
		{"oversize", true, map[string]any{"enabled": true, "max_body_bytes": 9 << 20}, false},
		{"invalid-wait", true, map[string]any{"enabled": true, "wait_timeout": "bad"}, false},
		{"unbounded-wait", true, map[string]any{"enabled": true, "wait_timeout": "31s"}, false},
		{"too-many-flights", true, map[string]any{"enabled": true, "max_flights": 129}, false},
		{"too-many-waiters", true, map[string]any{"enabled": true, "max_waiters": 129}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"include_query_in_cache_key": true, "representation": map[string]any{"enabled": tc.enabled}, "response_sharing": tc.sharing}})
			if cleanup != nil {
				defer cleanup()
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
			if err == nil && mw(proxy.Transport{Proxy: proxy.GetProxy()}) == nil {
				t.Fatal("missing public binding")
			}
		})
	}
}

func TestPublicResponseSharingColdGetUsesOneOrigin(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	_, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		originImage(w, "verified source bytes", "Accept", `"origin"`)
	})
	store, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", Representation: &conf.RepresentationStorage{StateDirectory: t.TempDir()}, Buckets: []*conf.Bucket{{Path: t.TempDir(), MaxObjectLimit: 100}}}, log.GetLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	storage.SetDefault(store)
	mw, cleanup, err := Middleware(&configv1.Middleware{Options: map[string]any{"include_query_in_cache_key": true, "representation": map[string]any{"enabled": true}, "response_sharing": map[string]any{"enabled": true, "wait_timeout": "5s"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	rt := mw(proxy.Transport{Proxy: proxy.GetProxy()})
	const count = 6
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			resp, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
			if err == nil && (body != "verified source bytes" || resp.Header.Get("Content-Type") != "image/jpeg") {
				err = fmt.Errorf("invalid public response: %q", body)
			}
			results <- err
		}()
	}
	// Observe actual registration through the public gauge, with no timing-based
	// batching delay or test-only hook in the implementation.
	deadline := time.Now().Add(3 * time.Second)
	for {
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		waiting := float64(0)
		for _, family := range families {
			if family.GetName() == "tr_tavern_response_sharing_waiters" {
				waiting = family.Metric[0].GetGauge().GetValue()
			}
		}
		if waiting == count-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public callers did not join verified sharing", waiting)
		}
		time.Sleep(time.Millisecond)
	}
	unblock()
	for i := 0; i < count; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("public configuration did not share cold response", calls.Load())
	}
	_, body, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Accept": {"image/jpeg"}}))
	if err != nil || body != "verified source bytes" || calls.Load() != 1 {
		t.Fatal("shared publication could not subsequently hit", body, err)
	}
}

// A canceled prebuffer must close and join its origin before storage Drain can
// report success. This body deliberately delays Close completion.
type sharingBlockedBody struct {
	entered, closeStarted, finishClose chan struct{}
	once, closeOnce                    sync.Once
}

func (b *sharingBlockedBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.closeStarted
	return 0, context.Canceled
}
func (b *sharingBlockedBody) Close() error {
	b.closeOnce.Do(func() { close(b.closeStarted); <-b.finishClose })
	return nil
}

func TestResponseSharingCancellationJoinsOriginClose(t *testing.T) {
	g := sharingTestGroup(t)
	req := sharingTestRequest(t)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	body := &sharingBlockedBody{entered: make(chan struct{}), closeStarted: make(chan struct{}), finishClose: make(chan struct{})}
	origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		resp := sharingTestResponse(r, "PNG")
		resp.Body = body
		return resp, nil
	})
	done := make(chan error, 1)
	go func() {
		resp, err := g.RoundTrip(origin, req.Clone(ctx))
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	<-body.entered
	cancel()
	<-body.closeStarted
	select {
	case <-done:
		t.Fatal("returned before actual source Close completed")
	default:
	}
	close(body.finishClose)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled prebuffer did not join")
	}
	assertSharingReleased(t, g)
}
