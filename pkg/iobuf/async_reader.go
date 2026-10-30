package iobuf

import (
	"io"
	"net/http"
	"sync"
)

// ProxyCallback fetches one response. It must honor the request's cancellation
// so Close can join the accepted fetch even if response headers are still pending.
type ProxyCallback func() (*http.Response, error)

type asyncBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *asyncBody) Close() error { b.once.Do(func() { b.err = b.ReadCloser.Close() }); return b.err }

type asyncReader struct {
	R      *io.PipeReader
	mu     sync.Mutex
	body   *asyncBody
	closed bool
	done   chan struct{}
}

// AsyncReadCloser performs a fetch in a goroutine. Close interrupts the active
// body and joins the goroutine before returning, including its writer callbacks.
func AsyncReadCloser(proxy ProxyCallback) io.ReadCloser {
	pr, pw := io.Pipe()
	ar := &asyncReader{R: pr, done: make(chan struct{})}
	go func() {
		defer close(ar.done)
		defer pw.Close()
		resp, err := proxy()
		if resp != nil && resp.Body != nil {
			body := &asyncBody{ReadCloser: resp.Body}
			ar.mu.Lock()
			ar.body = body
			closed := ar.closed
			ar.mu.Unlock()
			defer body.Close()
			if closed {
				return
			}
		}
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if resp == nil || resp.Body == nil {
			pw.CloseWithError(io.ErrUnexpectedEOF)
			return
		}
		if _, err = io.Copy(pw, resp.Body); err != nil {
			pw.CloseWithError(err)
		}
	}()
	return ar
}
func (r *asyncReader) Read(p []byte) (int, error) { return r.R.Read(p) }
func (r *asyncReader) Close() error {
	r.mu.Lock()
	r.closed = true
	body := r.body
	r.mu.Unlock()
	err := r.R.Close()
	if body != nil {
		body.Close()
	}
	<-r.done
	return err
}
