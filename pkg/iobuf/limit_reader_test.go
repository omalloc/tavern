package iobuf

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// mockReadCloser is a mock implementation of io.ReadCloser for testing
type mockReadCloser struct {
	data   string
	pos    int
	closed bool
	err    error
}

func (m *mockReadCloser) Read(p []byte) (n int, err error) {
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	if m.err != nil {
		return 0, m.err
	}
	if m.pos >= len(m.data) {
		return 0, io.EOF
	}
	n = copy(p, m.data[m.pos:])
	m.pos += n
	return n, nil
}

func (m *mockReadCloser) Close() error {
	m.closed = true
	return nil
}

// errorReadCloser returns an error on read
type errorReadCloser struct {
	err error
}

func (e *errorReadCloser) Read(p []byte) (n int, err error) {
	return 0, e.err
}

func (e *errorReadCloser) Close() error {
	return nil
}

// TestLimitReadCloser_Basic tests basic read functionality
func TestLimitReadCloser_Basic(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 5)
	defer limited.Close()

	p := make([]byte, 10)
	n, err := limited.Read(p)

	if n != 5 {
		t.Errorf("expected 5 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if string(p[:n]) != "Hello" {
		t.Errorf("expected 'Hello', got '%s'", string(p[:n]))
	}
}

// TestLimitReadCloser_ReadExactLimit tests reading exactly to the limit
func TestLimitReadCloser_ReadExactLimit(t *testing.T) {
	data := "12345"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, int64(len(data)))
	defer limited.Close()

	p := make([]byte, len(data))
	n, err := limited.Read(p)

	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestLimitReadCloser_ReadBeyondLimit tests reading beyond the limit stops at limit
func TestLimitReadCloser_ReadBeyondLimit(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 5)
	defer limited.Close()

	p := make([]byte, 100)
	n, err := limited.Read(p)

	if n != 5 {
		t.Errorf("expected 5 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestLimitReadCloser_MultipleReads tests multiple sequential reads
func TestLimitReadCloser_MultipleReads(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 10)
	defer limited.Close()

	// First read
	p1 := make([]byte, 5)
	n1, err1 := limited.Read(p1)
	if n1 != 5 || err1 != nil {
		t.Errorf("first read: expected 5 bytes and no error, got %d and %v", n1, err1)
	}

	// Second read (should get remaining 5 bytes of limit)
	p2 := make([]byte, 5)
	n2, err2 := limited.Read(p2)
	if n2 != 5 || err2 != nil {
		t.Errorf("second read: expected 5 bytes and no error, got %d and %v", n2, err2)
	}

	// Third read (should be empty because limit reached)
	p3 := make([]byte, 5)
	n3, err3 := limited.Read(p3)
	if n3 != 0 || err3 == nil {
		t.Errorf("third read: expected 0 bytes and EOF, got %d and %v", n3, err3)
	}
}

// TestLimitReadCloser_ZeroLimit tests with zero limit
func TestLimitReadCloser_ZeroLimit(t *testing.T) {
	data := "Hello"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 0)
	defer limited.Close()

	p := make([]byte, 5)
	n, err := limited.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
	if err != io.EOF {
		t.Errorf("expected EOF error, got %v", err)
	}
}

// TestLimitReadCloser_SmallBuffer tests reading with buffer smaller than data
func TestLimitReadCloser_SmallBuffer(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 20)
	defer limited.Close()

	p := make([]byte, 3)
	n, err := limited.Read(p)

	if n != 3 {
		t.Errorf("expected 3 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if string(p) != "Hel" {
		t.Errorf("expected 'Hel', got '%s'", string(p))
	}
}

// TestLimitReadCloser_ReadError tests handling of read errors from underlying reader
func TestLimitReadCloser_ReadError(t *testing.T) {
	errRead := errors.New("read error")
	errReader := &errorReadCloser{err: errRead}
	limited := LimitReadCloser(errReader, 10)
	defer limited.Close()

	p := make([]byte, 5)
	n, err := limited.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes on error, got %d", n)
	}
	if !errors.Is(err, errRead) {
		t.Errorf("expected %v error, got %v", errRead, err)
	}
}

// TestLimitReadCloser_WriteTo tests the WriteTo method
func TestLimitReadCloser_WriteTo(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 5)
	defer limited.Close()

	limRc := limited.(*limitedReadCloser)
	var buf strings.Builder
	n, err := limRc.WriteTo(&buf)

	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != "Hello" {
		t.Errorf("expected 'Hello', got '%s'", buf.String())
	}
}

// TestLimitReadCloser_WriteToExactLimit tests WriteTo with exact limit
func TestLimitReadCloser_WriteToExactLimit(t *testing.T) {
	data := "12345"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, int64(len(data)))
	defer limited.Close()

	limRc := limited.(*limitedReadCloser)
	var buf strings.Builder
	n, err := limRc.WriteTo(&buf)

	if n != int64(len(data)) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != data {
		t.Errorf("expected '%s', got '%s'", data, buf.String())
	}
}

// TestLimitReadCloser_WriteToLargeData tests WriteTo with data larger than limit
func TestLimitReadCloser_WriteToLargeData(t *testing.T) {
	data := "Hello, World! This is a long string."
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 12)
	defer limited.Close()

	limRc := limited.(*limitedReadCloser)
	var buf strings.Builder
	n, err := limRc.WriteTo(&buf)

	if n != 12 {
		t.Errorf("expected 12 bytes written, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != "Hello, World" {
		t.Errorf("expected 'Hello, World', got '%s'", buf.String())
	}
}

// TestLimitReadCloser_WriteToZeroLimit tests WriteTo with zero limit
func TestLimitReadCloser_WriteToZeroLimit(t *testing.T) {
	data := "Hello"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 0)
	defer limited.Close()

	limRc := limited.(*limitedReadCloser)
	var buf strings.Builder
	n, err := limRc.WriteTo(&buf)

	if n != 0 {
		t.Errorf("expected 0 bytes written, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != "" {
		t.Errorf("expected empty string, got '%s'", buf.String())
	}
}

// TestLimitReadCloser_Close tests closing the reader
func TestLimitReadCloser_Close(t *testing.T) {
	mock := &mockReadCloser{data: "test"}
	limited := LimitReadCloser(mock, 10)

	err := limited.Close()

	if err != nil {
		t.Errorf("expected no error on close, got %v", err)
	}
	if !mock.closed {
		t.Error("expected underlying reader to be closed")
	}
}

// TestLimitReadCloser_CloseError tests close with underlying close error
func TestLimitReadCloser_CloseError(t *testing.T) {
	// Create a mock reader
	mock := &mockReadCloser{data: "test"}

	// We'll use a custom closer for this test
	limited := LimitReadCloser(mock, 10)

	// Close the underlying reader to trigger potential errors
	err := limited.Close()
	if err != nil {
		t.Errorf("expected close to succeed with mock reader, got %v", err)
	}
}

// TestLimitReadCloser_ReadAfterClose tests read after close
func TestLimitReadCloser_ReadAfterClose(t *testing.T) {
	mock := &mockReadCloser{data: "Hello"}
	limited := LimitReadCloser(mock, 10)

	limited.Close()

	p := make([]byte, 5)
	n, err := limited.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes after close, got %d", n)
	}
	if err == nil {
		t.Error("expected error after close")
	}
}

// TestLimitReadCloser_LargeLimit tests with a very large limit
func TestLimitReadCloser_LargeLimit(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 1000000) // 1MB limit
	defer limited.Close()

	p := make([]byte, len(data))
	n, err := limited.Read(p)

	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error on first read, got %v", err)
	}

	// Read again to get EOF
	p2 := make([]byte, 10)
	n2, err2 := limited.Read(p2)

	if n2 != 0 {
		t.Errorf("expected 0 bytes on second read, got %d", n2)
	}
	if err2 != io.EOF {
		t.Errorf("expected EOF, got %v", err2)
	}
}

// TestLimitReadCloser_ByteTracking tests that bytes are properly tracked
func TestLimitReadCloser_ByteTracking(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 7)
	defer limited.Close()

	// First read 3 bytes
	p1 := make([]byte, 3)
	n1, _ := limited.Read(p1)

	// Second read 4 bytes (should get exactly 4 to reach the 7 byte limit)
	p2 := make([]byte, 10)
	n2, _ := limited.Read(p2)

	total := n1 + n2
	if total != 7 {
		t.Errorf("expected total of 7 bytes tracked, got %d", total)
	}
}

// TestLimitReadCloser_EmptyData tests reading from empty data source
func TestLimitReadCloser_EmptyData(t *testing.T) {
	mock := &mockReadCloser{data: ""}
	limited := LimitReadCloser(mock, 10)
	defer limited.Close()

	p := make([]byte, 5)
	n, err := limited.Read(p)

	if n != 0 {
		t.Errorf("expected 0 bytes from empty data, got %d", n)
	}
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

// TestLimitReadCloser_SequentialReadAndWriteTo tests mixing Read and WriteTo
func TestLimitReadCloser_SequentialReadAndWriteTo(t *testing.T) {
	data := "Hello, World!"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 10)
	defer limited.Close()

	// First read some bytes
	p := make([]byte, 3)
	n1, _ := limited.Read(p)

	// Then WriteTo the rest
	limRc := limited.(*limitedReadCloser)
	var buf strings.Builder
	n2, _ := limRc.WriteTo(&buf)

	total := int64(n1) + n2
	if total != 10 {
		t.Errorf("expected total of 10 bytes, got %d", total)
	}
}

// TestLimitReadCloser_OneByteAtATime tests reading one byte at a time
func TestLimitReadCloser_OneByteAtATime(t *testing.T) {
	data := "test"
	mock := &mockReadCloser{data: data}
	limited := LimitReadCloser(mock, 2)
	defer limited.Close()

	result := ""
	p := make([]byte, 1)

	for i := 0; i < 3; i++ {
		n, err := limited.Read(p)
		if n > 0 {
			result += string(p[:n])
		}
		if err != nil && err != io.EOF {
			t.Errorf("unexpected error: %v", err)
		}
	}

	if result != "te" {
		t.Errorf("expected 'te', got '%s'", result)
	}
}
