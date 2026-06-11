package caching

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ChunkFlightGroup tests
// ---------------------------------------------------------------------------

func TestChunkFlight_BasicCollapse(t *testing.T) {
	g := &ChunkFlightGroup{}
	var callCount atomic.Int32

	// fn returns a body equal in length to the requested range so callers
	// can verify their sub-range trimming works.
	fn := func(unionFrom, unionTo uint64) (*http.Response, error) {
		callCount.Add(1)
		size := int(unionTo - unionFrom + 1)
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Body:       io.NopCloser(bytes.NewReader(makebuf(size))),
		}, nil
	}

	type result struct {
		length int
		shared bool
	}

	results := make([]result, 3)
	var wg sync.WaitGroup
	start := make(chan struct{})

	// All three callers request the same range — classic collapse.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			r, shared, err := g.Do("obj1", 0, 1023, 50*time.Millisecond, fn)
			if err != nil {
				t.Errorf("caller %d: unexpected error: %v", idx, err)
				return
			}
			data, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil {
				t.Errorf("caller %d: read error: %v", idx, readErr)
				return
			}
			results[idx] = result{len(data), shared}
		}(i)
	}

	// Release all callers simultaneously so they race on the map entry.
	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", callCount.Load())
	}

	sharedCount := 0
	for _, r := range results {
		if r.shared {
			sharedCount++
		}
		if r.length != 1024 {
			t.Errorf("got %d bytes, want 1024", r.length)
		}
	}
	if sharedCount != 2 {
		t.Errorf("expected 2 shared callers, got %d", sharedCount)
	}
}

func TestChunkFlight_RangeUnion(t *testing.T) {
	g := &ChunkFlightGroup{}
	var callCount atomic.Int32

	fn := func(unionFrom, unionTo uint64) (*http.Response, error) {
		callCount.Add(1)
		size := int(unionTo - unionFrom + 1)
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Body:       io.NopCloser(bytes.NewReader(makebuf(size))),
		}, nil
	}

	type result struct {
		length int
		shared bool
	}

	type caller struct {
		from, to uint64
		wantLen  int
	}

	callers := []caller{
		{0, 999, 1000},     // bytes 0-999
		{500, 1999, 1500},  // bytes 500-1999, overlaps first
		{1500, 2999, 1500}, // bytes 1500-2999, overlaps second
	}
	results := make([]result, len(callers))
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i, c := range callers {
		wg.Add(1)
		go func(idx int, from, to uint64) {
			defer wg.Done()
			<-start
			r, shared, err := g.Do("union-obj", from, to, 50*time.Millisecond, fn)
			if err != nil {
				t.Errorf("caller %d: unexpected error: %v", idx, err)
				return
			}
			data, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil {
				t.Errorf("caller %d: read error: %v", idx, readErr)
				return
			}
			results[idx] = result{len(data), shared}
		}(i, c.from, c.to)
	}

	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()

	// With range union, all three callers share one origin fetch covering
	// the union range 0-2999.
	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call (union), got %d", callCount.Load())
	}

	for i, r := range results {
		if r.length != callers[i].wantLen {
			t.Errorf("caller %d: got %d bytes, want %d", i, r.length, callers[i].wantLen)
		}
	}

	sharedCount := 0
	for _, r := range results {
		if r.shared {
			sharedCount++
		}
	}
	if sharedCount != len(callers)-1 {
		t.Errorf("expected %d shared callers, got %d", len(callers)-1, sharedCount)
	}
}

func TestChunkFlight_ErrorPropagation(t *testing.T) {
	g := &ChunkFlightGroup{}

	fn := func(_, _ uint64) (*http.Response, error) {
		return nil, errors.New("upstream timeout")
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			r, _, err := g.Do("obj1", 0, 1023, 50*time.Millisecond, fn)
			if err != nil {
				errs[idx] = err
				return
			}
			_, readErr := io.ReadAll(r)
			_ = r.Close()
			if readErr != nil {
				errs[idx] = readErr
			}
		}(i)
	}

	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("caller %d: expected error, got nil", i)
		}
	}
}

func TestChunkFlight_KeyIsolation(t *testing.T) {
	g := &ChunkFlightGroup{}
	var callCount atomic.Int32

	makeFn := func(data string) func(uint64, uint64) (*http.Response, error) {
		return func(unionFrom, unionTo uint64) (*http.Response, error) {
			callCount.Add(1)
			size := int(unionTo - unionFrom + 1)
			return &http.Response{
				StatusCode: http.StatusPartialContent,
				Body:       io.NopCloser(bytes.NewReader(makebuf(size))),
			}, nil
		}
	}

	var wg sync.WaitGroup
	results := make(map[string]int, 4)
	var mu sync.Mutex

	// Two objects (obj1, obj2), each with two concurrent callers requesting
	// different ranges.  Within each object the ranges are unioned, but
	// different objects are isolated.
	type job struct {
		key          string
		from, to     uint64
		wantLen      int
	}
	jobs := []job{
		{"obj1", 0, 1048575, 1048576},
		{"obj1", 1048576, 2097151, 1048576},
		{"obj2", 0, 1048575, 1048576},
		{"obj2", 1048576, 2097151, 1048576},
	}
	for _, j := range jobs {
		wg.Add(1)
		go func(k string, from, to uint64) {
			defer wg.Done()
			r, _, err := g.Do(k, from, to, 50*time.Millisecond, makeFn(k))
			if err != nil {
				t.Errorf("key %s: unexpected error: %v", k, err)
				return
			}
			data, _ := io.ReadAll(r)
			_ = r.Close()
			mu.Lock()
			results[k] = len(data)
			mu.Unlock()
		}(j.key, j.from, j.to)
	}
	wg.Wait()

	// With object-level keys, obj1's two callers collapse into one
	// (union: 0-2097151), obj2's two callers collapse into another.
	if callCount.Load() != 2 {
		t.Fatalf("expected 2 calls (one per object), got %d", callCount.Load())
	}

	// Each caller must receive exactly their requested byte count.
	for _, j := range jobs {
		if results[j.key] != j.wantLen {
			t.Errorf("key %s: got %d bytes, want %d", j.key, results[j.key], j.wantLen)
		}
	}
}

func TestChunkFlight_ConcurrentSameKey(t *testing.T) {
	g := &ChunkFlightGroup{}
	var callCount atomic.Int32

	fn := func(_, _ uint64) (*http.Response, error) {
		callCount.Add(1)
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Body:       io.NopCloser(bytes.NewReader(makebuf(1 << 18))),
		}, nil
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	const numCallers = 10
	sharedCount := atomic.Int32{}

	for i := 0; i < numCallers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// All callers request the same range 0-262143.
			r, shared, err := g.Do("same-key", 0, 262143, 100*time.Millisecond, fn)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			_, _ = io.ReadAll(r)
			_ = r.Close()
			if shared {
				sharedCount.Add(1)
			}
		}()
	}

	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected exactly 1 origin call, got %d", callCount.Load())
	}
	if sharedCount.Load() != numCallers-1 {
		t.Fatalf("expected %d shared callers, got %d", numCallers-1, sharedCount.Load())
	}
}

func TestChunkFlight_PanicRecovery(t *testing.T) {
	g := &ChunkFlightGroup{}

	pr, shared, err := g.Do("panic-key", 0, 1023, 0, func(_, _ uint64) (*http.Response, error) {
		panic("boom")
	})
	if shared {
		t.Fatal("expected leader, not shared")
	}

	// fn is now called asynchronously — the leader gets the error through
	// the pipe, not from the Do return value.
	if err != nil {
		t.Fatalf("unexpected error from Do: %v", err)
	}

	_, readErr := io.ReadAll(pr)
	_ = pr.Close()
	if readErr == nil || !strings.Contains(readErr.Error(), "panic") {
		t.Fatalf("expected panic error from pipe, got %v", readErr)
	}
}

// ---------------------------------------------------------------------------
// ObjectFlightGroup tests
// ---------------------------------------------------------------------------

func TestObjectFlight_BasicCollapse(t *testing.T) {
	g := &ObjectFlightGroup{}
	var callCount atomic.Int32

	fn := func() (*http.Response, error) {
		callCount.Add(1)
		time.Sleep(30 * time.Millisecond) // simulate origin latency
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("response-body")),
		}, nil
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	bodies := make([]string, 5)
	shareds := make([]bool, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			resp, shared, err := g.Do("cache-key-1", 50*time.Millisecond, fn)
			if err != nil {
				t.Errorf("caller %d: unexpected error: %v", idx, err)
				return
			}
			shareds[idx] = shared
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Errorf("caller %d: read error: %v", idx, readErr)
				return
			}
			bodies[idx] = string(body)
		}(i)
	}

	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", callCount.Load())
	}
	nonShared := 0
	shared := 0
	for _, s := range shareds {
		if s {
			shared++
		} else {
			nonShared++
		}
	}
	if nonShared != 1 {
		t.Errorf("expected 1 non-shared caller, got %d", nonShared)
	}
	if shared != 4 {
		t.Errorf("expected 4 shared callers, got %d", shared)
	}
	for i, b := range bodies {
		if b != "response-body" {
			t.Errorf("caller %d: body = %q, want %q", i, b, "response-body")
		}
	}
}

func TestObjectFlight_ErrorPropagation(t *testing.T) {
	g := &ObjectFlightGroup{}
	var callCount atomic.Int32

	testErr := errors.New("origin connection refused")
	fn := func() (*http.Response, error) {
		callCount.Add(1)
		time.Sleep(30 * time.Millisecond) // window for dup callers to register
		return nil, testErr
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, _, err := g.Do("cache-key-err", 50*time.Millisecond, fn)
			errs[idx] = err
		}(i)
	}

	time.Sleep(10 * time.Millisecond)
	close(start)
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", callCount.Load())
	}
	for i, err := range errs {
		if !errors.Is(err, testErr) {
			t.Errorf("caller %d: got %v, want %v", i, err, testErr)
		}
	}
}

func TestObjectFlight_KeyIsolation(t *testing.T) {
	g := &ObjectFlightGroup{}
	var callCount atomic.Int32

	fn := func() (*http.Response, error) {
		callCount.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("body")),
		}, nil
	}

	var wg sync.WaitGroup
	for _, key := range []string{"key-a", "key-b", "key-c"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			resp, _, err := g.Do(k, 0, fn)
			if err != nil {
				t.Errorf("key %s: unexpected error: %v", k, err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(key)
	}
	wg.Wait()

	if callCount.Load() != 3 {
		t.Fatalf("expected 3 distinct calls, got %d", callCount.Load())
	}
}

func TestObjectFlight_SequentialReuse(t *testing.T) {
	g := &ObjectFlightGroup{}
	var callCount atomic.Int32

	fn := func() (*http.Response, error) {
		callCount.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("body")),
		}, nil
	}

	resp, _, err := g.Do("seq-key", 0, fn)
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if callCount.Load() != 1 {
		t.Fatalf("first call: expected 1, got %d", callCount.Load())
	}

	time.Sleep(10 * time.Millisecond)

	resp, _, err = g.Do("seq-key", 0, fn)
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if callCount.Load() != 2 {
		t.Fatalf("sequential call: expected 2, got %d", callCount.Load())
	}
}
