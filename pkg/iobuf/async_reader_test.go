package iobuf

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAsyncReadCloser_SuccessfulRead tests successful async reading
func TestAsyncReadCloser_SuccessfulRead(t *testing.T) {
	data := "Hello, World!"
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	p := make([]byte, 100)
	n, err := reader.Read(p)

	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if string(p[:n]) != data {
		t.Errorf("expected '%s', got '%s'", data, string(p[:n]))
	}
}

// TestAsyncReadCloser_ProxyCallbackError tests handling of proxy callback error
func TestAsyncReadCloser_ProxyCallbackError(t *testing.T) {
	callbackErr := errors.New("proxy error")
	callback := func() (*http.Response, error) {
		return nil, callbackErr
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	// Give some time for async operation
	time.Sleep(10 * time.Millisecond)

	p := make([]byte, 100)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes on error, got %d", n)
	}
	if !errors.Is(err, callbackErr) {
		t.Errorf("expected %v error, got %v", callbackErr, err)
	}
}

// TestAsyncReadCloser_NilResponse tests handling of nil response
func TestAsyncReadCloser_NilResponse(t *testing.T) {
	callback := func() (*http.Response, error) {
		return nil, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	// Give some time for async operation
	time.Sleep(10 * time.Millisecond)

	p := make([]byte, 100)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes on nil response, got %d", n)
	}
	if err != io.ErrUnexpectedEOF {
		t.Errorf("expected ErrUnexpectedEOF, got %v", err)
	}
}

// TestAsyncReadCloser_NilBody tests handling of response with nil body
func TestAsyncReadCloser_NilBody(t *testing.T) {
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: nil,
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	// Give some time for async operation
	time.Sleep(10 * time.Millisecond)

	p := make([]byte, 100)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes on nil body, got %d", n)
	}
	if err != io.ErrUnexpectedEOF {
		t.Errorf("expected ErrUnexpectedEOF, got %v", err)
	}
}

// TestAsyncReadCloser_MultipleReads tests multiple sequential reads
func TestAsyncReadCloser_MultipleReads(t *testing.T) {
	data := "Hello, World!"
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	// First read
	p1 := make([]byte, 5)
	n1, err1 := reader.Read(p1)

	if n1 != 5 || err1 != nil {
		t.Errorf("first read: expected 5 bytes and no error, got %d and %v", n1, err1)
	}

	// Second read
	p2 := make([]byte, 8)
	n2, err2 := reader.Read(p2)

	if n2 != 8 || err2 != nil {
		t.Errorf("second read: expected 8 bytes and no error, got %d and %v", n2, err2)
	}

	// Third read (should get EOF)
	p3 := make([]byte, 5)
	n3, err3 := reader.Read(p3)

	if n3 != 0 || err3 != io.EOF {
		t.Errorf("third read: expected 0 bytes and EOF, got %d and %v", n3, err3)
	}
}

// TestAsyncReadCloser_SmallBuffer tests reading with small buffer
func TestAsyncReadCloser_SmallBuffer(t *testing.T) {
	data := "Hello"
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	p := make([]byte, 2)
	n, err := reader.Read(p)

	if n != 2 {
		t.Errorf("expected 2 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if string(p) != "He" {
		t.Errorf("expected 'He', got '%s'", string(p))
	}
}

// TestAsyncReadCloser_LargeBuffer tests reading with large buffer
func TestAsyncReadCloser_LargeBuffer(t *testing.T) {
	data := "test"
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	p := make([]byte, 1000)
	n, err := reader.Read(p)

	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestAsyncReadCloser_EmptyResponse tests reading from empty response
func TestAsyncReadCloser_EmptyResponse(t *testing.T) {
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader("")),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	p := make([]byte, 100)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes from empty response, got %d", n)
	}
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

// TestAsyncReadCloser_CopyError tests handling of copy error from body
func TestAsyncReadCloser_CopyError(t *testing.T) {
	copyErr := errors.New("copy error")
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: &asyncErrorReader{err: copyErr},
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	// Give some time for async operation
	time.Sleep(10 * time.Millisecond)

	p := make([]byte, 100)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes on copy error, got %d", n)
	}
	if !errors.Is(err, copyErr) {
		t.Errorf("expected %v error, got %v", copyErr, err)
	}
}

// TestAsyncReadCloser_Close tests closing the reader
func TestAsyncReadCloser_Close(t *testing.T) {
	data := "test"
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)

	err := reader.Close()

	if err != nil {
		t.Errorf("expected no error on close, got %v", err)
	}
}

// TestAsyncReadCloser_ReadAfterClose tests read after close
func TestAsyncReadCloser_ReadAfterClose(t *testing.T) {
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader("test")),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	reader.Close()

	p := make([]byte, 5)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes after close, got %d", n)
	}
	if err != io.EOF && err != io.ErrClosedPipe {
		t.Logf("got error: %v (acceptable)", err)
	}
}

// TestAsyncReadCloser_LargeData tests reading large amounts of data
func TestAsyncReadCloser_LargeData(t *testing.T) {
	data := strings.Repeat("x", 10000)
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	result := bytes.Buffer{}
	n, err := io.Copy(&result, reader)

	if int(n) != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if result.String() != data {
		t.Errorf("data mismatch: expected %d bytes, got %d", len(data), result.Len())
	}
}

// TestAsyncReadCloser_ZeroByteRead tests reading zero bytes
func TestAsyncReadCloser_ZeroByteRead(t *testing.T) {
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader("test")),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	p := make([]byte, 0)
	n, err := reader.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes from zero-length buffer, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestAsyncReadCloser_ResponseBodyClosing tests that response body is properly closed
func TestAsyncReadCloser_ResponseBodyClosing(t *testing.T) {
	closed := false
	closeFunc := func() error {
		closed = true
		return nil
	}

	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: &mockCloser{Reader: strings.NewReader("test"), close: closeFunc},
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)

	// Read all data
	p := make([]byte, 100)
	reader.Read(p)

	// Give time for goroutine to finish
	time.Sleep(50 * time.Millisecond)

	reader.Close()

	if !closed {
		t.Error("expected response body to be closed")
	}
}

// TestAsyncReadCloser_ErrorReturned tests that error is returned to reader
func TestAsyncReadCloser_ErrorReturned(t *testing.T) {
	callbackErr := errors.New("callback failed")
	callback := func() (*http.Response, error) {
		return nil, callbackErr
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	time.Sleep(20 * time.Millisecond)

	p := make([]byte, 100)
	_, err := reader.Read(p)

	if err == nil {
		t.Error("expected error to be returned")
	}
}

// TestAsyncReadCloser_ReadBeyondAvailable tests reading beyond available data
func TestAsyncReadCloser_ReadBeyondAvailable(t *testing.T) {
	data := "test"
	callback := func() (*http.Response, error) {
		resp := &http.Response{
			Body: io.NopCloser(strings.NewReader(data)),
		}
		return resp, nil
	}

	reader := AsyncReadCloser(callback)
	defer reader.Close()

	p := make([]byte, 1000)
	n, err := reader.Read(p)

	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// Try to read again
	n2, err2 := reader.Read(p)

	if n2 != 0 {
		t.Errorf("expected 0 bytes on second read, got %d", n2)
	}
	if err2 != io.EOF {
		t.Errorf("expected EOF, got %v", err2)
	}
}

// asyncErrorReader is a mock reader that returns an error
type asyncErrorReader struct {
	err error
}

func (e *asyncErrorReader) Read(p []byte) (n int, err error) {
	return 0, e.err
}

func (e *asyncErrorReader) Close() error {
	return nil
}

// mockCloser is a mock io.ReadCloser with custom close function
type mockCloser struct {
	io.Reader
	close func() error
}

func (m *mockCloser) Close() error {
	return m.close()
}
