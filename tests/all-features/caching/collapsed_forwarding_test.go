package caching

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/pkg/e2e"
	"github.com/stretchr/testify/require"
)

// The CI server deliberately retains the old collapsed_request=true setting.
// It must not restore unverified response sharing. The opt-in v2 feature has its
// own public disk/Pebble acceptance tests alongside the caching implementation.
func TestLegacyCollapseConfigurationDoesNotCrossResponses(t *testing.T) {
	for _, mode := range []string{"vary", "wildcard", "no-store"} {
		t.Run(mode, func(t *testing.T) {
			const count = 4
			var calls atomic.Int32
			entered := make(chan struct{}, count)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			endpoint := "http://legacy-collapse.example/" + mode
			server := e2e.New(endpoint, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Cache-Control", "max-age=600")
				w.Header().Set("Vary", "Accept")
				if mode == "wildcard" {
					w.Header().Set("Vary", "*")
				}
				if mode == "no-store" {
					w.Header().Set("Cache-Control", "max-age=600,no-store")
				}
				w.Header().Set("Content-Type", r.Header.Get("Accept"))
				body := fmt.Sprintf("%s:%d", r.Header.Get("Accept"), n)
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				io.WriteString(w, body)
			})
			t.Cleanup(server.Close)
			results := make(chan error, count)
			for i := 0; i < count; i++ {
				mime := "image/jpeg"
				if mode == "vary" && i%2 == 1 {
					mime = "image/webp"
				}
				go func() {
					resp, err := server.Do(func(r *http.Request) { r.Header.Set("Accept", mime) })
					if err == nil {
						data, readErr := io.ReadAll(resp.Body)
						resp.Body.Close()
						err = readErr
						if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != mime || len(data) < len(mime)+2 || string(data[:len(mime)+1]) != mime+":" {
							err = fmt.Errorf("crossed response for %s: code=%d body=%q", mime, resp.StatusCode, data)
						}
					}
					results <- err
				}()
			}
			for i := 0; i < count; i++ {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("legacy option shared an unverified response")
				}
			}
			unblock()
			for i := 0; i < count; i++ {
				require.NoError(t, <-results)
			}
			require.Equal(t, int32(count), calls.Load())
		})
	}
}

func TestIndependentMissRetainsSequentialHitsAndURLIsolation(t *testing.T) {
	file := e2e.GenFile(t, 2<<20)
	var calls atomic.Int32
	server := e2e.New("http://independent-miss.example/sequential", e2e.RespCallbackFile(file, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	for i := 0; i < 2; i++ {
		resp, err := server.Do(func(*http.Request) {})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := e2e.HashBody(resp)
		resp.Body.Close()
		require.Equal(t, file.MD5, got)
	}
	require.Equal(t, int32(1), calls.Load(), "ordinary sequential hits remain enabled")
	for _, suffix := range []string{"a", "b", "c"} {
		resp, err := server.Do(func(r *http.Request) { r.URL.Path += "/" + suffix })
		require.NoError(t, err)
		got := e2e.HashBody(resp)
		resp.Body.Close()
		require.Equal(t, file.MD5, got)
	}
	require.Equal(t, int32(4), calls.Load(), "distinct URLs must not reuse another cache key")
}

func TestIndependentMissingRangesPreserveEveryByte(t *testing.T) {
	file := e2e.GenFile(t, 3<<20)
	const count = 3
	var calls atomic.Int32
	var concurrent atomic.Bool
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := e2e.New("http://independent-range.example/partial", e2e.RespCallbackFile(file, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if concurrent.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("ETag", fmt.Sprintf("%q", file.MD5))
	}))
	t.Cleanup(server.Close)
	// Only the middle chunk is warm; every following request needs missing bytes.
	resp, err := server.Do(func(r *http.Request) { r.Header.Set("Range", "bytes=1048576-1572863") })
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	calls.Store(0)
	concurrent.Store(true)
	type span struct{ start, end int }
	spans := []span{{0, 524287}, {0, 1048575}, {1572864, 2097151}}
	results := make(chan error, count)
	for _, interval := range spans {
		go func() {
			resp, err := server.Do(func(r *http.Request) { r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", interval.start, interval.end)) })
			if err == nil {
				got := e2e.HashBody(resp)
				resp.Body.Close()
				wantRange := fmt.Sprintf("bytes %d-%d/%d", interval.start, interval.end, file.Size)
				if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != wantRange || resp.ContentLength != int64(interval.end-interval.start+1) || got != e2e.HashFile(file.Path, interval.start, interval.end-interval.start+1) {
					err = fmt.Errorf("incorrect independent range %s: status=%d length=%d hash=%s", wantRange, resp.StatusCode, resp.ContentLength, got)
				}
			}
			results <- err
		}()
	}
	for i := 0; i < count; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("missing ranges were unsafely combined")
		}
	}
	unblock()
	for i := 0; i < count; i++ {
		require.NoError(t, <-results)
	}
	require.Equal(t, int32(count), calls.Load())
}
