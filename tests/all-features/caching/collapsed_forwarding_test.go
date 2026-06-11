package caching

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omalloc/tavern/pkg/e2e"
)

func TestCollapsedForwardingObjectFlight(t *testing.T) {
	f := e2e.GenFile(t, 2<<20)

	t.Run("test Collapsed Forwarding ObjectFlight Collapse", func(t *testing.T) {

		var originCallCount atomic.Int32

		case1 := e2e.New("http://objflight.example.com/of/object/collapse.bin", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			originCallCount.Add(1)
			time.Sleep(80 * time.Millisecond) // window for concurrent registrations

			t.Logf("X-Request-Idx: %s", r.Header.Get("X-Request-Idx"))

			w.Header().Set("Cache-Control", "max-age=10")
			w.Header().Set("ETag", "obj-flight-etag")
		}))
		defer case1.Close()

		const N = 5
		var wg sync.WaitGroup
		start := make(chan struct{}, N)
		bodies := make([]string, N)
		codes := make([]int, N)
		xCaches := make([]string, N)

		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start

				t.Logf("started for caller %d", idx)

				resp, err := case1.Do(func(r *http.Request) {
					r.Header.Set("X-Request-Idx", strconv.Itoa(idx))
				})

				require.NoError(t, err, "caller %d: request should not error", idx)
				defer resp.Body.Close()

				hash := e2e.HashBody(resp)

				bodies[idx] = hash
				codes[idx] = resp.StatusCode
				xCaches[idx] = resp.Header.Get("X-Cache")
			}(i)
		}

		time.Sleep(10 * time.Millisecond)
		close(start)
		wg.Wait()

		// Verify only one origin call — ObjectFlightGroup collapsed all 5.
		assert.Equal(t, int32(1), originCallCount.Load(),
			"object flight should collapse concurrent full-MISS requests")

		// All callers must receive identical response bodies.
		for i := 0; i < N; i++ {
			assert.Equal(t, http.StatusOK, codes[i], "caller %d: status mismatch", i)
			assert.Equal(t, f.MD5, bodies[i], "caller %d: body mismatch", i)
		}

		// At least one should be MISS (the first), the rest may be HIT
		// depending on whether they re-looked up metadata in time.
		hasMiss := false
		for _, c := range xCaches {
			if c != "" {
				hasMiss = hasMiss || strings.Contains(c, "MISS")
			}
		}
		assert.True(t, hasMiss, "at least one response should report MISS")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://objflight.example.com/of/object/collapse.bin")
	})

	t.Run("test Collapsed Forwarding ObjectFlight Sequential", func(t *testing.T) {
		var originCallCount atomic.Int32

		originCallCount.Store(0)

		case1 := e2e.New("http://objflight.example.com/of/object/sequential.bin", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			originCallCount.Add(1)

			w.Header().Set("Cache-Control", "max-age=10")
			w.Header().Set("ETag", "obj-flight-etag")
		}))
		defer case1.Close()

		const N = 3

		bodies := make([]string, N)

		// Sequential requests should not be collapsed.
		for i := 0; i < N; i++ {
			t.Logf("starting request %d", i)

			resp, err := case1.Do(func(r *http.Request) {
				r.Header.Set("X-Request-Idx", strconv.Itoa(i))
			})

			require.NoError(t, err, "request %d should not error", i)
			bodies[i] = e2e.HashBody(resp)

			resp.Body.Close()
		}

		assert.Equal(t, int32(1), originCallCount.Load(),
			"object flight should not collapse sequential requests")

		for i := 0; i < N; i++ {
			assert.Equal(t, f.MD5, bodies[i], "request %d body-hash mismatch", i)
		}

	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://objflight.example.com/of/object/sequential.bin")
	})

	t.Run("test Collapsed Forwarding ObjectFlight KeyIsolation", func(t *testing.T) {
		var originCallCount atomic.Int32

		case1 := e2e.New("http://keys.example.com/of/object/", func(w http.ResponseWriter, r *http.Request) {
			originCallCount.Add(1)
			time.Sleep(80 * time.Millisecond)

			w.Header().Set("Cache-Control", "max-age=10")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(r.URL.Path))
		})
		defer case1.Close()

		keys := []string{"key-a", "key-b", "key-c"}

		var wg sync.WaitGroup
		start := make(chan struct{}, len(keys))

		for _, key := range keys {
			wg.Add(1)
			go func(k string) {
				defer wg.Done()
				<-start

				resp, err := case1.Do(func(r *http.Request) {
					r.URL.Path += k
					t.Logf("Requesting key: %s", k)
				})

				require.NoError(t, err)
				buf, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				assert.Equal(t, "/of/object/"+k, string(buf), "response body should match requested key")
			}(key)
		}

		time.Sleep(10 * time.Millisecond)
		close(start)
		wg.Wait()

		// Three different keys → three independent origin calls.
		assert.Equal(t, int32(3), originCallCount.Load(),
			"different URLs should have independent object flights")

	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.PurgeMethod(t, "http://keys.example.com/of/object/", true)
	})

}

func TestCollapsedForwardingChunkFlight(t *testing.T) {
	file := e2e.GenFile(t, 3<<20) // 3MB → 6 chunks at 512KB

	t.Run("test Collapsed Forwarding ChunkFlight", func(t *testing.T) {
		var originCallCount atomic.Int32

		case1 := e2e.New("http://chunkflight.example.com/cf/chunk/collapse.bin", e2e.RespCallbackFile(file, func(w http.ResponseWriter, r *http.Request) {
			originCallCount.Add(1)

			w.Header().Set("Cache-Control", "max-age=30")
			w.Header().Set("ETag", file.MD5)
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusPartialContent, resp.StatusCode)

		// Give storage time to finish writing indexdb metadata.
		time.Sleep(300 * time.Millisecond)

		// Phase 2 — concurrent requests for a range that needs missing chunks.
		originCallCount.Store(0)

		const N = 3
		var wg sync.WaitGroup
		start := make(chan struct{}, N)
		bodies := make([]string, N)
		codes := make([]int, N)
		xCaches := make([]string, N)
		ranges := make([]string, N)
		cls := make([]string, N)

		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start

				t.Logf("started for caller %d", idx)

				resp1, err1 := case1.Do(func(r *http.Request) {
					r.Header.Set("Range", "bytes=524288-2097151")
				})

				require.NoError(t, err1, "caller %d: request should not error", idx)
				defer resp1.Body.Close()

				hashStr := e2e.HashBody(resp1)

				bodies[idx] = string(hashStr)
				codes[idx] = resp1.StatusCode
				xCaches[idx] = resp1.Header.Get("X-Cache")
				ranges[idx] = resp1.Header.Get("Content-Range")
				cls[idx] = strconv.Itoa(int(resp1.ContentLength))
			}(i)
		}

		time.Sleep(10 * time.Millisecond)
		close(start)
		wg.Wait()

		t.Logf("origin call count for concurrent phase: %d", originCallCount.Load())

		// All callers must receive correct 206 responses.
		for i := 0; i < N; i++ {
			assert.Equal(t, http.StatusPartialContent, codes[i], "caller %d: status mismatch", i)
			assert.NotEmpty(t, bodies[i], "caller %d: body should not be empty", i)

			t.Logf("caller %d: hash: %s range: %s X-Cache: %s Content-Length: %s", i, bodies[i], ranges[i], xCaches[i], cls[i])
		}

		// Verify body correctness: compare against the source file.
		expected := e2e.HashFile(file.Path, 524288, 2097151-524288+1)
		for i := 0; i < N; i++ {
			actual := bodies[i]
			assert.Equal(t, expected, actual, "caller %d: body hash mismatch", i)
		}

		// The concurrent chunk fetch for the missing range must be collapsed.
		assert.Equal(t, int32(1), originCallCount.Load(),
			"chunk flight should collapse concurrent chunk fetches to 1 origin call")
	})

	t.Run("test Collapsed Forwarding ChunkFlight KeyIsolation", func(t *testing.T) {
		var originCallCount atomic.Int32

		case1 := e2e.New("http://chunkflight.example.com/cf/chunk/keys.bin", e2e.RespCallbackFile(file, func(w http.ResponseWriter, r *http.Request) {
			t.Logf("process req %s, range %s", r.Header.Get("X-Request-Id"), r.Header.Get("Range"))
			originCallCount.Add(1)

			w.Header().Set("Cache-Control", "max-age=30")
			w.Header().Set("ETag", file.MD5)
		}))
		defer case1.Close()

		// Phase 1 — cache only the middle chunk (chunk 1, bytes 524288-1048575).
		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("X-Request-Id", "0")
			r.Header.Set("Range", "bytes=524288-1048575")
		})

		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusPartialContent, resp.StatusCode)

		originCallCount.Store(0)

		time.Sleep(300 * time.Millisecond)

		// Phase 2 — request two different missing ranges concurrently.
		// Range A: bytes=0-524287 (needs chunk 0, not cached)
		// Range B: bytes=1048576-2097151 (needs chunk 2+, not cached)
		// With range union, these two ranges collapse into one origin
		// fetch for the union range bytes=0-2097151.
		var wg sync.WaitGroup

		ranges := []string{"bytes=0-524287", "bytes=1048576-2097151"}
		start := make(chan struct{}, len(ranges))
		errs := make([]error, len(ranges))
		xCaches := make([]string, len(ranges))

		for i, rng := range ranges {
			wg.Add(1)
			go func(idx int, rng string) {
				defer wg.Done()
				<-start

				resp2, e := case1.Do(func(r *http.Request) {
					t.Logf("started for caller %d with range %s", idx+1, rng)

					r.Header.Set("X-Request-Id", strconv.Itoa(idx+1))
					r.Header.Set("Range", rng)
				})
				if e != nil {
					t.Logf("caller %d: request error: %v", idx+1, e)
					errs[idx] = e
					return
				}

				io.Copy(io.Discard, resp2.Body)
				resp2.Body.Close()

				xCaches[idx] = resp2.Header.Get("X-Cache")
			}(i, rng)
		}

		time.Sleep(10 * time.Millisecond)
		close(start)
		wg.Wait()

		for i, e := range errs {
			assert.NoError(t, e, "request %d should not error", i)
			t.Logf("caller %d X-Cache: %s", i+1, xCaches[i])
		}

		// With range union, the two different ranges are collapsed into
		// one origin fetch covering the union bytes=0-2097151.
		assert.Equal(t, int32(1), originCallCount.Load(),
			"different byte ranges should be collapsed via range union")

	})

	t.Run("test Collapsed Forwarding ChunkFlight RangeUnion", func(t *testing.T) {
		var originCallCount atomic.Int32

		case1 := e2e.New("http://chunkflight.example.com/cf/chunk/union.bin", e2e.RespCallbackFile(file, func(w http.ResponseWriter, r *http.Request) {
			t.Logf("process req %s, range %s", r.Header.Get("X-Request-Id"), r.Header.Get("Range"))
			originCallCount.Add(1)

			w.Header().Set("Cache-Control", "max-age=30")
			w.Header().Set("ETag", file.MD5)
		}))
		defer case1.Close()

		// Phase 1 — cache only the middle chunk (chunk 1, bytes 524288-1048575).
		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("X-Request-Id", "0")
			r.Header.Set("Range", "bytes=524288-1048575")
		})

		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusPartialContent, resp.StatusCode)

		originCallCount.Store(0)

		time.Sleep(300 * time.Millisecond)

		// Phase 2 — three concurrent requests for different ranges that
		// need missing chunks.  With range union, all three share a single
		// origin fetch whose range covers the union of all requested ranges.
		type reqSpec struct {
			id     string
			rng    string
			from   int
			length int
		}
		specs := []reqSpec{
			{"1", "bytes=0-524287", 0, 524288},
			{"2", "bytes=0-1048575", 0, 1048576},
			{"3", "bytes=1048576-2097151", 1048576, 2097151 - 1048576 + 1},
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, len(specs))
		hashes := make([]string, len(specs))
		xCaches := make([]string, len(specs))

		for i, spec := range specs {
			wg.Add(1)
			go func(idx int, s reqSpec) {
				defer wg.Done()
				<-start

				resp2, e := case1.Do(func(r *http.Request) {
					r.Header.Set("X-Request-Id", s.id)
					r.Header.Set("Range", s.rng)
				})
				if e != nil {
					t.Logf("caller %s: request error: %v", s.id, e)
					errs[idx] = e
					return
				}

				hashes[idx] = e2e.HashBody(resp2)
				resp2.Body.Close()

				xCaches[idx] = resp2.Header.Get("X-Cache")
			}(i, spec)
		}

		time.Sleep(10 * time.Millisecond)
		close(start)
		wg.Wait()

		for i, e := range errs {
			assert.NoError(t, e, "request %d should not error", i)
			t.Logf("caller %s X-Cache: %s hash: %s", specs[i].id, xCaches[i], hashes[i])
		}

		// All three different ranges should collapse into one origin call via
		// automatic range union.
		assert.Equal(t, int32(1), originCallCount.Load(),
			"range union should collapse different ranges into 1 origin call")

		// Each caller must receive the correct bytes for its range.
		for i, spec := range specs {
			expected := e2e.HashFile(file.Path, spec.from, spec.length)
			assert.Equal(t, expected, hashes[i],
				"caller %s (range %s): body hash mismatch", spec.id, spec.rng)
		}
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.SetDump(true)
		e2e.Purge(t, "http://chunkflight.example.com/cf/chunk/collapse.bin")
		e2e.Purge(t, "http://chunkflight.example.com/cf/chunk/keys.bin")
		e2e.Purge(t, "http://chunkflight.example.com/cf/chunk/union.bin")
	})
}
