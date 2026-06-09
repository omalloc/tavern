package caching

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// objectFlightCall represents an in-flight full-object origin fetch.
//
// Unlike the previous WaitGroup-only approach, this uses io.Pipe +
// io.MultiWriter to fan out the response body to all concurrent callers.
// This ensures the leader's response body is consumed (which drives the
// SavepartAsyncReader → disk writes) while simultaneously providing data
// to all waiting callers — no cache re-lookup is needed.
type objectFlightCall struct {
	resp  *http.Response
	pipes []*io.PipeWriter
	mu    sync.Mutex     // protects pipes during registration and snapshot
	wg    sync.WaitGroup // signals that resp headers / err are ready
	err   error
}

// ObjectFlightGroup collapses concurrent full-MISS requests for the same
// cache object.  Unlike ChunkFlightGroup (which works at the chunk/segment
// level), this operates at the whole-object level — it ensures only one
// goroutine hits origin for a given cache key.
//
// The returned response carries the headers from the leader's fn and a
// body that fans out to all concurrent callers.  Callers must close the
// body.
type ObjectFlightGroup struct {
	mu sync.Mutex
	m  map[string]*objectFlightCall
}

// Do executes fn once per key and fans out the response body to all
// concurrent callers.  All callers receive the same response headers
// (cloned) and a shared body stream.
//
// waiter is the duration the leader pauses before calling fn, giving
// late-arriving callers a window to register under the same key.
//
// Returns:
//
//	resp  — a response carrying the leader's headers and a shared body
//	shared — true if this caller joined an existing flight
//	err    — error from fn or from body copy
func (g *ObjectFlightGroup) Do(key string, waiter time.Duration, fn func() (*http.Response, error)) (*http.Response, bool, error) {
	pr, pw := io.Pipe()

	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*objectFlightCall)
	}
	if c, ok := g.m[key]; ok {
		// Waiter: register a pipe writer and wait for headers.
		c.mu.Lock()
		c.pipes = append(c.pipes, pw)
		c.mu.Unlock()
		g.mu.Unlock()

		c.wg.Wait()
		if c.err != nil {
			_ = pw.CloseWithError(c.err)
			return nil, true, c.err
		}

		resp := cloneResponse(c.resp)
		resp.Body = pr
		return resp, true, nil
	}

	// Leader: create the flight and execute fn.
	c := &objectFlightCall{pipes: []*io.PipeWriter{pw}}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	if waiter > 0 {
		time.Sleep(waiter)
	}

	// check for panic to avoid leaving waiters hanging indefinitely
	resp, err := func() (r *http.Response, e error) {
		defer func() {
			if rec := recover(); rec != nil {
				e = fmt.Errorf("object flight panic: %v", rec)
			}
		}()
		return fn()
	}()

	g.mu.Lock()
	delete(g.m, key)

	if err != nil {
		c.err = err
		g.mu.Unlock()
		c.wg.Done()

		// Snapshot pipes under c.mu to avoid racing with waiter registrations.
		c.mu.Lock()
		for _, p := range c.pipes {
			_ = p.CloseWithError(err)
		}
		c.mu.Unlock()
		return nil, false, err
	}

	c.resp = resp
	c.wg.Done() // release waiters — headers are now available

	// Snapshot pipes under c.mu to avoid racing with waiter registrations.
	c.mu.Lock()
	pipes := make([]*io.PipeWriter, len(c.pipes))
	copy(pipes, c.pipes)
	c.mu.Unlock()
	g.mu.Unlock()

	// Fan out the response body to all pipes (including the leader's).
	// This also drives the SavepartAsyncReader → disk write chain.
	go func() {
		writers := make([]io.Writer, len(pipes))
		for i, p := range pipes {
			writers[i] = p
		}
		mw := io.MultiWriter(writers...)

		var copyErr error
		if resp.Body != nil {
			_, copyErr = io.Copy(mw, resp.Body)
			_ = resp.Body.Close()
		}

		for _, p := range pipes {
			if copyErr != nil && copyErr != io.EOF {
				_ = p.CloseWithError(copyErr)
			} else {
				_ = p.Close()
			}
		}
	}()

	leaderResp := cloneResponse(resp)
	leaderResp.Body = pr
	return leaderResp, false, nil
}

// cloneResponse returns a shallow copy of resp with a cloned Header map.
// Body is left nil — the caller sets it to a pipe reader.
func cloneResponse(resp *http.Response) *http.Response {
	if resp == nil {
		return nil
	}
	return &http.Response{
		Status:           resp.Status,
		StatusCode:       resp.StatusCode,
		Proto:            resp.Proto,
		ProtoMajor:       resp.ProtoMajor,
		ProtoMinor:       resp.ProtoMinor,
		Header:           resp.Header.Clone(),
		ContentLength:    resp.ContentLength,
		TransferEncoding: resp.TransferEncoding,
		Close:            resp.Close,
		Uncompressed:     resp.Uncompressed,
		Request:          resp.Request,
		TLS:              resp.TLS,
	}
}
