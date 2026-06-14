package caching

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/omalloc/tavern/pkg/iobuf"
)

// chunkRange holds one caller's desired byte range within the object.
type chunkRange struct {
	fromByte, toByte uint64
}

// chunkCall is an in-flight chunk upstream request.  Multiple callers
// register their desired ranges; the leader computes the union and
// fetches a single range that covers everyone.  Each caller receives
// their own sub-range trimmed via iobuf.RangeReader.
type chunkCall struct {
	pipes     []*io.PipeWriter
	ranges    []chunkRange
	mu        sync.Mutex     // protects pipes and ranges during registration
	wg        sync.WaitGroup // signals that unionFrom / unionTo are computed
	unionFrom uint64
	unionTo   uint64
	err       error // set when fn fails
}

// ChunkFlightGroup collapses concurrent upstream requests for different
// byte ranges of the same object into a single origin fetch covering the
// union of all requested ranges.  Response body bytes are fanned out to
// all waiters via io.MultiWriter + io.Pipe, and each caller trims the
// shared stream down to its own sub-range with iobuf.RangeReader.
//
// This mirrors Squid's collapsed_forwarding at the chunk/segment level:
// when two goroutines request different byte ranges of the same cached
// object, only one hits origin and the others wait, even when the
// ranges differ.
type ChunkFlightGroup struct {
	mu sync.Mutex
	m  map[string]*chunkCall
}

// Do executes fn once per objectKey, passing the union of all registered
// byte ranges to fn.  All callers — including the first — receive an
// io.PipeReader trimmed to exactly [fromByte, toByte].  The returned
// bool reports whether this caller shared an in-flight request.
//
// waiter is the duration the leader goroutine pauses *before* calling fn,
// giving late-arriving callers a window to register under the same key.
// In production the network round-trip naturally provides this window;
// waiter ensures correctness even when fn would otherwise complete nearly
// instantly (e.g. in tests, or for tiny ranges on a local origin).
//
// Contract: fn owns resp.Body.  On success ChunkFlightGroup reads and
// closes it.  On error fn must either return (nil, err) or close the body
// before returning (resp, err).
func (g *ChunkFlightGroup) Do(objectKey string, fromByte, toByte uint64, waiter time.Duration, fn func(unionFrom, unionTo uint64) (*http.Response, error)) (io.ReadCloser, bool, error) {
	pr, pw := io.Pipe()

	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*chunkCall)
	}
	if c, ok := g.m[objectKey]; ok {
		// Waiter: register pipe writer and desired range, then wait for
		// the leader to compute the union range.
		c.mu.Lock()
		c.pipes = append(c.pipes, pw)
		c.ranges = append(c.ranges, chunkRange{fromByte: fromByte, toByte: toByte})
		c.mu.Unlock()
		g.mu.Unlock()

		c.wg.Wait()
		c.mu.Lock()
		flightErr := c.err
		c.mu.Unlock()
		if flightErr != nil {
			_ = pw.CloseWithError(flightErr)
			return nil, true, flightErr
		}
		// Return immediately — fn is executed asynchronously by the
		// leader goroutine, so the caller can build response headers
		// before c.md is mutated by the upstream fetch.
		return iobuf.RangeReader(pr, int(c.unionFrom), int(c.unionTo), int(fromByte), int(toByte)), true, nil
	}

	// Leader: create the flight and register own range.
	c := &chunkCall{
		pipes:  []*io.PipeWriter{pw},
		ranges: []chunkRange{{fromByte: fromByte, toByte: toByte}},
	}
	c.wg.Add(1)
	g.m[objectKey] = c
	g.mu.Unlock()

	// Pause before hitting origin so concurrent callers have time
	// to register under this key.  Without this window an instant
	// fn would compute the union and delete the map entry before
	// anyone else could join.
	if waiter > 0 {
		time.Sleep(waiter)
	}

	// Compute the union range across all registered callers.
	c.mu.Lock()
	unionFrom := c.ranges[0].fromByte
	unionTo := c.ranges[0].toByte
	for _, r := range c.ranges[1:] {
		if r.fromByte < unionFrom {
			unionFrom = r.fromByte
		}
		if r.toByte > unionTo {
			unionTo = r.toByte
		}
	}
	c.unionFrom = unionFrom
	c.unionTo = unionTo
	c.mu.Unlock()

	// Release waiters — they now know unionFrom/unionTo and can build
	// their RangeReader wrappers around the shared pipe.  fn has not
	// been called yet, so callers can safely read c.md headers before
	// the upstream fetch mutates them.
	c.wg.Done()

	// Remove the key from the map so that no late-arriving callers can
	// join this flight with a stale union range.  The waiter window
	// (time.Sleep above) is the intentional batching period — callers
	// arriving after it will start a fresh flight.  This trades a
	// possible duplicate origin request for guaranteed range correctness.
	g.mu.Lock()
	delete(g.m, objectKey)
	g.mu.Unlock()

	// The leader returns immediately with a pipe reader.  The upstream
	// fetch (fn) and body fan-out run in a background goroutine so that
	// response headers are built before c.md is touched.
	go func() {

		// check for panic to avoid leaving waiters hanging indefinitely
		resp, err := func() (r *http.Response, e error) {
			defer func() {
				if rec := recover(); rec != nil {
					e = fmt.Errorf("chunk flight panic: %v", rec)
				}
			}()
			return fn(unionFrom, unionTo)
		}()

		// Snapshot pipes under c.mu.  The map entry is already deleted
		// (above), so no further callers can register against this
		// flight — c.mu only protects against the window where waiters
		// that registered before the deletion are still being appended.
		c.mu.Lock()
		pipes := make([]*io.PipeWriter, len(c.pipes))
		copy(pipes, c.pipes)
		if err != nil {
			c.err = err
		}
		c.mu.Unlock()

		if err != nil {
			for _, p := range pipes {
				_ = p.CloseWithError(err)
			}
			// fn owns resp.Body on error — it must close it before
			// returning.  We only guard against a nil body here.
			return
		}

		// Build MultiWriter from all registered pipe writers.
		writers := make([]io.Writer, len(pipes))
		for i, p := range pipes {
			writers[i] = p
		}
		mw := io.MultiWriter(writers...)

		_, copyErr := io.Copy(mw, resp.Body)
		_ = resp.Body.Close()

		for _, p := range pipes {
			if copyErr != nil && copyErr != io.EOF {
				_ = p.CloseWithError(copyErr)
			} else {
				_ = p.Close()
			}
		}
	}()

	// Leader wraps its reader with RangeReader so that it sees exactly
	// [fromByte, toByte] trimmed from the union response.
	return iobuf.RangeReader(pr, int(unionFrom), int(unionTo), int(fromByte), int(toByte)), false, nil
}
