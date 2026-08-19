package caching

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var objectFlightControlHeaders = map[string]struct{}{
	"range":                       {},
	"if-range":                    {},
	"if-match":                    {},
	"if-none-match":               {},
	"if-modified-since":           {},
	"if-unmodified-since":         {},
	"authorization":               {},
	"proxy-authorization":         {},
	"cookie":                      {},
	"cache-control":               {},
	"pragma":                      {},
	protocol.InternalUpstreamAddr: {},
}

// objectFlightCall represents an in-flight full-object origin fetch.
//
// Unlike the previous WaitGroup-only approach, this uses io.Pipe +
// io.MultiWriter to fan out the response body to all concurrent callers.
// This ensures the leader's response body is consumed (which drives the
// SavepartAsyncReader → disk writes) while simultaneously providing data
// to all waiting callers — no cache re-lookup is needed.
type objectFlightCall struct {
	resp         *http.Response
	leaderHeader http.Header
	waiters      []*objectFlightWaiter
	wg           sync.WaitGroup // signals that resp headers / err are ready
	err          error
}

type objectFlightWaiter struct {
	header   http.Header
	writer   *io.PipeWriter
	retryKey string
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

func objectFlightRequestKey(base string, req *http.Request) string {
	selected := make(http.Header, len(objectFlightControlHeaders))
	for name, values := range req.Header {
		if _, ok := objectFlightControlHeaders[strings.ToLower(name)]; ok {
			selected[name] = values
		}
	}

	return base + "\x00method:" + req.Method + "\x00control:" + xhttp.HeaderFingerprint(selected)
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
	return g.do(key, nil, waiter, fn)
}

// DoRequest is like Do, but also uses the response's Vary fields to avoid
// sharing a representation with a request whose selected header values differ.
// Requests rejected from the initial flight are collapsed again under a
// representation-specific key.
func (g *ObjectFlightGroup) DoRequest(key string, header http.Header, waiter time.Duration, fn func() (*http.Response, error)) (*http.Response, bool, error) {
	return g.do(key, header, waiter, fn)
}

func (g *ObjectFlightGroup) do(key string, header http.Header, waiter time.Duration, fn func() (*http.Response, error)) (*http.Response, bool, error) {
	pr, pw := io.Pipe()
	w := &objectFlightWaiter{header: header.Clone(), writer: pw}

	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*objectFlightCall)
	}
	if c, ok := g.m[key]; ok {
		// Waiter: register a pipe writer and wait for headers.
		c.waiters = append(c.waiters, w)
		g.mu.Unlock()

		c.wg.Wait()
		if c.err != nil {
			_ = pw.CloseWithError(c.err)
			return nil, true, c.err
		}
		if w.retryKey != "" {
			_ = pw.Close()
			_ = pr.Close()
			return g.do(w.retryKey, header, waiter, fn)
		}

		resp := cloneResponse(c.resp)
		resp.Body = pr
		return resp, true, nil
	}

	// Leader: create the flight and execute fn.
	c := &objectFlightCall{
		leaderHeader: header.Clone(),
		waiters:      []*objectFlightWaiter{w},
	}
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

		for _, waiter := range c.waiters {
			_ = waiter.writer.CloseWithError(err)
		}
		return nil, false, err
	}

	c.resp = resp
	pipes := make([]*io.PipeWriter, 0, len(c.waiters))
	for i, waiter := range c.waiters {
		retryKey, compatible := objectFlightVaryMatch(key, c.leaderHeader, waiter.header, resp, i)
		if compatible {
			pipes = append(pipes, waiter.writer)
			continue
		}
		waiter.retryKey = retryKey
	}
	c.wg.Done() // release waiters — headers and Vary matching are ready
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

func objectFlightVaryMatch(key string, leader, waiter http.Header, resp *http.Response, position int) (string, bool) {
	if resp == nil {
		return "", true
	}

	varyKey := varycontrol.Clean(resp.Header.Values("Vary")...)
	if len(varyKey) == 0 {
		return "", true
	}

	if varyKey.HasWildcard() {
		if position == 0 {
			return "", true
		}
		return fmt.Sprintf("%s\x00vary:*:%d", key, position), false
	}

	leaderVaryData := varyKey.VaryData(leader)
	waiterVaryData := varyKey.VaryData(waiter)
	if leaderVaryData == waiterVaryData {
		return "", true
	}
	return key + "\x00vary:" + waiterVaryData, false
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
