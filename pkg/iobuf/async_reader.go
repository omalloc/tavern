package iobuf

import (
	"io"
	"net/http"
)

// ProxyCallback defines a function type that returns an HTTP response and an error when called.
// It is used by [AsyncReadCloser] to perform the upstream fetch in a background goroutine.
type ProxyCallback func() (*http.Response, error)

// asyncReader wraps an io.PipeReader and captures write-side errors from the background
// goroutine so they can be surfaced on the next Read call.
type asyncReader struct {
	R   io.ReadCloser
	err error
}

// AsyncReadCloser returns an io.ReadCloser that invokes proxy in a background goroutine
// and pipes the response body back to the caller through an io.Pipe.
//
// The proxy callback is expected to fetch an upstream HTTP response. On success the
// response body is copied to the pipe writer; on failure the pipe is closed with the
// error so the caller's next Read returns it.
//
// This is used in the caching middleware to issue a sub-request to origin without
// blocking the caller's goroutine — the HTTP round-trip happens asynchronously while
// the caller can start reading the body as soon as bytes arrive.
func AsyncReadCloser(proxy ProxyCallback) io.ReadCloser {
	pr, pw := io.Pipe()

	ar := &asyncReader{R: pr}
	go func() {
		resp, err := proxy()
		defer func() {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			_ = pw.Close()
		}()

		if err != nil {
			ar.err = err
			_ = pw.CloseWithError(err)
			return
		}

		if resp == nil || resp.Body == nil {
			// ar.err = io.ErrUnexpectedEOF
			_ = pw.CloseWithError(io.ErrUnexpectedEOF)
			return
		}

		_, err1 := io.Copy(pw, resp.Body)
		if err1 != nil {
			ar.err = err1
			_ = pw.CloseWithError(err1)
		}
	}()

	return ar
}

// Read reads from the underlying pipe. If the pipe Writer was closed with an error
// (e.g. proxy callback failed), the stored error is surfaced here even when the
// underlying Read returns (0, nil) — a known quirk of io.Pipe when the write side fails.
func (r *asyncReader) Read(p []byte) (n int, err error) {
	n, err = r.R.Read(p)
	if err == io.EOF {
		return n, err
	}
	if err != nil {
		return n, err
	}
	if r.err != nil {
		return n, r.err
	}
	return n, nil
}

// Close closes the pipe reader. The pipe writer side (and the upstream response body)
// are already closed by the background goroutine's defer.
func (r *asyncReader) Close() error {
	if r.R != nil {
		return r.R.Close()
	}
	return nil
}
