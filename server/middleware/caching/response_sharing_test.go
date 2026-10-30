package caching

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/proxy/selector"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/server/middleware"
)

func sharingTestRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "GET", "https://example.com/a?x=1&x=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "image/png")
	return scopeResponseRequest(req, &object.ResourceRecord{ResourceID: "resource", Incarnation: "inc", BarrierIncarnation: "catalog", ActiveLocation: "disk", PurgeEpoch: 1, RevalidateEpoch: 2, PrefixHardEpoch: 3, PrefixSoftEpoch: 4, RoutingEpoch: 5})
}
func sharingTestResponse(req *http.Request, data string) *http.Response {
	return &http.Response{StatusCode: 200, ContentLength: int64(len(data)), Request: req,
		Header: http.Header{"Cache-Control": {"max-age=600"}, "Content-Length": {strconv.Itoa(len(data))}, "Content-Type": {"image/png"}, "Vary": {"Accept, X-Flavor"}, "Age": {"10"}}, Body: io.NopCloser(strings.NewReader(data))}
}
func sharingTestGroup(t *testing.T) *responseSharing {
	t.Helper()
	o := &cachingOption{ResponseSharing: &responseSharingOption{Enabled: true}, representation: &representationLookup{writes: true}}
	if err := o.bindResponseSharing(); err != nil {
		t.Fatal(err)
	}
	return o.sharing
}
func waitSharingUsers(t *testing.T, g *responseSharing, count int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		g.mu.Lock()
		users := 0
		for _, call := range g.calls {
			users += call.users
		}
		g.mu.Unlock()
		if users == count {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("flight users=%d, want=%d", users, count)
		case <-tick.C:
		}
	}
}
func assertSharingReleased(t *testing.T, g *responseSharing) {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active != 0 || len(g.calls) != 0 {
		t.Fatal("sharing lifetime leaked", g.active, len(g.calls))
	}
}

func TestCanReuseResponseRequiresActualRequestAndResponseProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*http.Request, *http.Response)
	}{
		{"actual-vary", func(r *http.Request, _ *http.Response) { r.Header.Set("Accept", "image/jpeg") }},
		{"non-vary-header", func(r *http.Request, _ *http.Response) { r.Header.Set("X-Unlisted", "different") }},
		{"missing-vs-empty", func(r *http.Request, _ *http.Response) { r.Header["X-Flavor"] = []string{""} }},
		{"ordered-lines", func(r *http.Request, _ *http.Response) { r.Header["Accept"] = []string{"image/png", "image/jpeg;q=0"} }},
		{"host", func(r *http.Request, _ *http.Response) { r.Host = "other.example" }},
		{"query-order", func(r *http.Request, _ *http.Response) { r.URL.RawQuery = "x=2&x=1" }},
		{"head", func(r *http.Request, _ *http.Response) { r.Method = "HEAD" }},
		{"range", func(r *http.Request, _ *http.Response) { r.Header["Range"] = nil }},
		{"conditional", func(r *http.Request, _ *http.Response) { r.Header.Set("If-None-Match", `"one"`) }},
		{"cookie", func(r *http.Request, _ *http.Response) { r.Header["Cookie"] = nil }},
		{"authorization", func(r *http.Request, _ *http.Response) { r.Header["Authorization"] = nil }},
		{"request-no-store", func(r *http.Request, _ *http.Response) { r.Header.Set("Cache-Control", "no-store") }},
		{"request-no-cache", func(r *http.Request, _ *http.Response) { r.Header.Set("Cache-Control", "no-cache") }},
		{"body", func(r *http.Request, _ *http.Response) { r.Body = io.NopCloser(strings.NewReader("input")) }},
		{"scope", func(r *http.Request, _ *http.Response) {
			s := r.Context().Value(responseScopeKey{}).(responseScope)
			s.Purge++
			*r = *r.WithContext(context.WithValue(r.Context(), responseScopeKey{}, s))
		}},
		{"peer", func(r *http.Request, _ *http.Response) {
			*r = *r.WithContext(selector.NewPeerContext(r.Context(), selector.NewPeer(selector.NewNode("http", "other:80", nil))))
		}},
		{"snapshot-budget", func(r *http.Request, _ *http.Response) { r.Header.Set("X-Large", strings.Repeat("x", 16384)) }},
		{"wildcard", func(_ *http.Request, r *http.Response) { r.Header.Set("Vary", "*") }},
		{"invalid-vary", func(_ *http.Request, r *http.Response) { r.Header.Set("Vary", "x bad") }},
		{"private", func(_ *http.Request, r *http.Response) { r.Header.Set("Cache-Control", "max-age=600, private") }},
		{"no-store", func(_ *http.Request, r *http.Response) { r.Header.Set("Cache-Control", "max-age=600, no-store") }},
		{"no-cache", func(_ *http.Request, r *http.Response) { r.Header.Set("Cache-Control", "max-age=600, no-cache") }},
		{"stale", func(_ *http.Request, r *http.Response) { r.Header.Set("Age", "601") }},
		{"set-cookie", func(_ *http.Request, r *http.Response) { r.Header.Set("Set-Cookie", "session=a") }},
		{"partial", func(_ *http.Request, r *http.Response) { r.StatusCode = 206 }},
		{"not-modified", func(_ *http.Request, r *http.Response) { r.StatusCode = 304 }},
		{"unknown-size", func(_ *http.Request, r *http.Response) { r.ContentLength = -1; r.Header.Del("Content-Length") }},
		{"duplicate-size", func(_ *http.Request, r *http.Response) {
			r.Header.Add("Content-Length", r.Header.Get("Content-Length"))
		}},
		{"trailer", func(_ *http.Request, r *http.Response) { r.Trailer = http.Header{"Digest": nil} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leader := sharingTestRequest(t)
			follower := leader.Clone(leader.Context())
			resp := sharingTestResponse(leader, "actual PNG bytes")
			if !CanReuseResponse(leader, follower, resp) {
				t.Fatal("equivalent requests rejected")
			}
			tc.change(follower, resp)
			before := resp.Header.Clone()
			if CanReuseResponse(leader, follower, resp) {
				t.Fatal("unsafe reuse allowed")
			}
			if !reflect.DeepEqual(before, resp.Header) {
				t.Fatal("proof mutated response headers")
			}
			resp.Body.Close()
		})
	}
}

func TestResponseSharingEqualRequestsOwnEveryResponse(t *testing.T) {
	g := sharingTestGroup(t)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return sharingTestResponse(r, "complete PNG bytes"), nil
	})
	const count = 8
	type result struct {
		resp *http.Response
		err  error
	}
	results := make(chan result, count)
	req := sharingTestRequest(t)
	go func() { r, e := g.RoundTrip(origin, req.Clone(req.Context())); results <- result{r, e} }()
	<-entered
	for i := 1; i < count; i++ {
		go func() { r, e := g.RoundTrip(origin, req.Clone(req.Context())); results <- result{r, e} }()
	}
	waitSharingUsers(t, g, count)
	unblock()
	responses := make([]*http.Response, count)
	for i := range responses {
		v := <-results
		if v.err != nil {
			t.Fatal(v.err)
		}
		responses[i] = v.resp
	}
	responses[0].Header.Set("Content-Type", "changed")
	responses[0].Request.Header.Set("Accept", "changed")
	for i, resp := range responses {
		age, err := strconv.Atoi(resp.Header.Get("Age"))
		if err != nil || age < 10 {
			t.Fatal("shared response reset origin age", resp.Header)
		}
		if i > 0 && (resp.Header.Get("Content-Type") != "image/png" || resp.Request.Header.Get("Accept") != "image/png") {
			t.Fatal("mutable response state shared")
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body.Close()
		if err != nil || string(data) != "complete PNG bytes" {
			t.Fatal(string(data), err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("equivalent requests did not merge", calls.Load())
	}
	assertSharingReleased(t, g)
}

func TestResponseSharingRejectedHeadersAndBodiesFetchIndependently(t *testing.T) {
	for _, mode := range []string{"wildcard", "private", "no-store", "cookie", "large", "unknown-length", "trailer", "short", "long", "error"} {
		t.Run(mode, func(t *testing.T) {
			g := sharingTestGroup(t)
			g.config.MaxBodyBytes = 32
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				n := calls.Add(1)
				if n == 1 {
					close(entered)
					<-release
				}
				if mode == "error" && n == 1 {
					return nil, errors.New("origin failed")
				}
				resp := sharingTestResponse(r, fmt.Sprintf("origin-%d", n))
				switch mode {
				case "wildcard":
					resp.Header.Set("Vary", "*")
				case "private":
					resp.Header.Set("Cache-Control", "private, max-age=600")
				case "no-store":
					resp.Header.Set("Cache-Control", "no-store, max-age=600")
				case "cookie":
					resp.Header.Set("Set-Cookie", "session=secret")
				case "large":
					resp.ContentLength = 64
					resp.Header.Set("Content-Length", "64")
					resp.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 64)))
				case "unknown-length":
					resp.ContentLength = -1
					resp.Header.Del("Content-Length")
				case "trailer":
					resp.Trailer = http.Header{"Digest": nil}
				case "short":
					resp.ContentLength = 12
					resp.Header.Set("Content-Length", "12")
				case "long":
					resp.ContentLength = 3
					resp.Header.Set("Content-Length", "3")
				}
				return resp, nil
			})
			const count = 4
			results := make(chan error, count)
			var readFailures atomic.Int32
			run := func() {
				resp, err := g.RoundTrip(origin, sharingTestRequest(t))
				if resp != nil {
					data, readErr := io.ReadAll(resp.Body)
					if readErr != nil {
						readFailures.Add(1)
					}
					resp.Body.Close()
					if mode != "short" && mode != "long" && readErr != nil {
						err = readErr
					}
					if mode == "long" && !bytes.HasPrefix(data, []byte("orig")) {
						err = fmt.Errorf("leader lost prefetched bytes: %q", data)
					}
				}
				results <- err
			}
			go run()
			<-entered
			for i := 1; i < count; i++ {
				go run()
			}
			waitSharingUsers(t, g, count)
			unblock()
			errorsSeen := 0
			for i := 0; i < count; i++ {
				if err := <-results; err != nil {
					errorsSeen++
				}
			}
			if mode == "error" && errorsSeen != 1 || mode != "error" && errorsSeen != 0 {
				t.Fatal("unexpected request errors", errorsSeen)
			}
			if calls.Load() != count {
				t.Fatal("unverified response was shared", calls.Load())
			}
			if (mode == "short" || mode == "long") && readFailures.Load() != 1 {
				t.Fatal("leader lost its terminal body error", readFailures.Load())
			}
			assertSharingReleased(t, g)
		})
	}
}

func TestResponseSharingFollowerCancellationAndTimeout(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			g := sharingTestGroup(t)
			g.config.WaitTimeout = "100ms"
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return sharingTestResponse(r, "PNG"), nil
			})
			leaderDone := make(chan error, 1)
			req := sharingTestRequest(t)
			go func() {
				resp, err := g.RoundTrip(origin, req)
				if resp != nil {
					resp.Body.Close()
				}
				leaderDone <- err
			}()
			<-entered
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			followerDone := make(chan error, 1)
			go func() {
				resp, err := g.RoundTrip(origin, req.Clone(ctx))
				if resp != nil {
					resp.Body.Close()
				}
				followerDone <- err
			}()
			waitSharingUsers(t, g, 2)
			if canceled {
				cancel()
			}
			err := <-followerDone
			if canceled && !errors.Is(err, context.Canceled) || !canceled && err != nil {
				t.Fatal(err)
			}
			// The leader remains live until explicitly released by this test.
			select {
			case <-leaderDone:
				t.Fatal("follower ended leader")
			default:
			}
			want := int32(2)
			if canceled {
				want = 1
			}
			if calls.Load() != want {
				t.Fatal("unexpected independent retries", calls.Load())
			}
			unblock()
			if err = <-leaderDone; err != nil {
				t.Fatal(err)
			}
			assertSharingReleased(t, g)
		})
	}
}

func TestResponseSharingBudgetsIncludeUnconsumedBodies(t *testing.T) {
	g := sharingTestGroup(t)
	g.config.MaxFlights = 1
	var calls atomic.Int32
	origin := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) { calls.Add(1); return sharingTestResponse(r, "PNG"), nil })
	req := sharingTestRequest(t)
	first, err := g.RoundTrip(origin, req)
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	active := g.active
	g.mu.Unlock()
	if active != 1 {
		t.Fatal("buffer permit released before response Close")
	}
	second, err := g.RoundTrip(origin, req)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	g.mu.Lock()
	active = g.active
	g.mu.Unlock()
	if active != 1 {
		t.Fatal("capacity fallback acquired an extra buffer")
	}
	first.Body.Close()
	assertSharingReleased(t, g)
	if calls.Load() != 2 {
		t.Fatal("completed flight became a second cache")
	}
}

func TestResponseSharingWaiterLimitAndPanicRelease(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprintf("panic=%t", panics), func(t *testing.T) {
			g := sharingTestGroup(t)
			g.config.MaxWaiters = 1
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			origin := middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					if panics {
						panic("origin panic")
					}
				}
				return sharingTestResponse(req, "PNG"), nil
			})
			leader := make(chan any, 1)
			req := sharingTestRequest(t)
			go func() {
				defer func() {
					if v := recover(); v != nil {
						leader <- v
					}
				}()
				resp, err := g.RoundTrip(origin, req)
				if resp != nil {
					resp.Body.Close()
				}
				leader <- err
			}()
			<-entered
			follower := make(chan error, 1)
			go func() {
				resp, err := g.RoundTrip(origin, req.Clone(req.Context()))
				if resp != nil {
					resp.Body.Close()
				}
				follower <- err
			}()
			waitSharingUsers(t, g, 2)
			if !panics {
				resp, err := g.RoundTrip(origin, req.Clone(req.Context()))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if calls.Load() != 2 {
					t.Fatal("waiter budget failed to bypass independently")
				}
			}
			unblock()
			result := <-leader
			if (result != nil) != panics {
				t.Fatal("unexpected leader result", result)
			}
			if err := <-follower; err != nil {
				t.Fatal("follower retained leader panic", err)
			}
			assertSharingReleased(t, g)
		})
	}
}
