package caching

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// chunkCall is an in-flight chunk upstream request.
type chunkCall struct {
	pipes []*io.PipeWriter
}

// ChunkFlightGroup collapses concurrent upstream requests for the same
// (object, byte-range) into a single origin fetch. Response body bytes
// are fanned out to all waiters via io.MultiWriter + io.Pipe.
//
// This mirrors Squid's collapsed_forwarding at the chunk/segment level:
// when two goroutines request the same byte range of the same cached
// object, only one hits origin and the others wait.
type ChunkFlightGroup struct {
	mu sync.Mutex
	m  map[string]*chunkCall
}

// Do executes fn once per key.  All callers — including the first — receive
// an io.PipeReader carrying the upstream response body.  The returned bool
// reports whether this caller shared an in-flight request.
//
// waiter is the duration the origin goroutine pauses *before* calling fn,
// giving late-arriving callers a window to register under the same key.
// In production the network round-trip naturally provides this window;
// waiter ensures correctness even when fn would otherwise complete nearly
// instantly (e.g. in tests, or for tiny ranges on a local origin).
//
// Contract: fn owns resp.Body.  On success ChunkFlightGroup reads and
// closes it.  On error fn must either return (nil, err) or close the body
// before returning (resp, err).
func (g *ChunkFlightGroup) Do(key string, waiter time.Duration, fn func() (*http.Response, error)) (io.ReadCloser, bool, error) {
	pr, pw := io.Pipe()

	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*chunkCall)
	}
	if c, ok := g.m[key]; ok {
		c.pipes = append(c.pipes, pw)
		g.mu.Unlock()
		return pr, true, nil
	}

	c := &chunkCall{pipes: []*io.PipeWriter{pw}}
	g.m[key] = c
	g.mu.Unlock()

	go func() {
		// Pause before hitting origin so concurrent callers have time
		// to register under this key.  Without this window an instant
		// fn would complete and delete the map entry before anyone
		// else could join.
		if waiter > 0 {
			time.Sleep(waiter)
		}

		// check for panic to avoid leaving waiters hanging indefinitely
		resp, err := func() (r *http.Response, e error) {
			defer func() {
				if rec := recover(); rec != nil {
					e = fmt.Errorf("chunk flight panic: %v", rec)
				}
			}()
			return fn()
		}()

		g.mu.Lock()
		// Snapshot pipes and remove the key so no further callers
		// register against this flight.
		pipes := make([]*io.PipeWriter, len(c.pipes))
		copy(pipes, c.pipes)
		delete(g.m, key)

		if err != nil {
			g.mu.Unlock()
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
		g.mu.Unlock()

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

	return pr, false, nil
}
