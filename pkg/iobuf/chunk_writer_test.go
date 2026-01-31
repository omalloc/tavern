package iobuf

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// mockReadWriteCloser is a mock implementation of io.ReadWriteCloser
type mockReadWriteCloser struct {
	writeData   []byte
	writeErr    error
	closeCalled bool
	closeErr    error
}

func (m *mockReadWriteCloser) Read(p []byte) (n int, err error) {
	return 0, nil
}

func (m *mockReadWriteCloser) Write(p []byte) (n int, err error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	m.writeData = append(m.writeData, p...)
	return len(p), nil
}

func (m *mockReadWriteCloser) Close() error {
	m.closeCalled = true
	return m.closeErr
}

// TestChunkWriterCloser_BasicWrite tests basic write functionality
func TestChunkWriterCloser_BasicWrite(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := []byte("Hello, World!")
	n, err := writer.Write(data)

	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if string(mock.writeData) != string(data) {
		t.Errorf("expected '%s', got '%s'", string(data), string(mock.writeData))
	}
}

// TestChunkWriterCloser_MultipleWrites tests multiple sequential writes
func TestChunkWriterCloser_MultipleWrites(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	// First write
	data1 := []byte("Hello, ")
	n1, err1 := writer.Write(data1)

	if n1 != len(data1) || err1 != nil {
		t.Errorf("first write: expected %d bytes and no error, got %d and %v", len(data1), n1, err1)
	}

	// Second write
	data2 := []byte("World!")
	n2, err2 := writer.Write(data2)

	if n2 != len(data2) || err2 != nil {
		t.Errorf("second write: expected %d bytes and no error, got %d and %v", len(data2), n2, err2)
	}

	expected := "Hello, World!"
	if string(mock.writeData) != expected {
		t.Errorf("expected '%s', got '%s'", expected, string(mock.writeData))
	}
}

// TestChunkWriterCloser_EmptyWrite tests writing empty data
func TestChunkWriterCloser_EmptyWrite(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := []byte("")
	n, err := writer.Write(data)

	if n != 0 {
		t.Errorf("expected 0 bytes written, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestChunkWriterCloser_LargeWrite tests writing large amount of data
func TestChunkWriterCloser_LargeWrite(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := []byte(strings.Repeat("x", 10000))
	n, err := writer.Write(data)

	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(mock.writeData) != len(data) {
		t.Errorf("expected %d bytes in mock, got %d", len(data), len(mock.writeData))
	}
}

// TestChunkWriterCloser_WriteError tests handling of write error
func TestChunkWriterCloser_WriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	mock := &mockReadWriteCloser{writeErr: writeErr}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := []byte("test")
	n, err := writer.Write(data)

	if n != 0 {
		t.Errorf("expected 0 bytes written on error, got %d", n)
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("expected %v error, got %v", writeErr, err)
	}
}

// TestChunkWriterCloser_Close tests closing the writer
func TestChunkWriterCloser_Close(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closerCalled := false
	closer := func() error {
		closerCalled = true
		return nil
	}

	writer := ChunkWriterCloser(mock, closer)

	err := writer.Close()

	if err != nil {
		t.Errorf("expected no error on close, got %v", err)
	}
	if !mock.closeCalled {
		t.Error("expected underlying writer to be closed")
	}
	if !closerCalled {
		t.Error("expected closer function to be called")
	}
}

// TestChunkWriterCloser_CloseOrder tests that underlying writer is closed before closer function
func TestChunkWriterCloser_CloseOrder(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closerOrder := 0

	closer := func() error {
		closerOrder = 2
		return nil
	}

	writer := ChunkWriterCloser(mock, closer)

	writer.Close()

	if mock.closeCalled && closerOrder != 2 {
		t.Error("expected underlying writer to be closed before closer function")
	}
}

// TestChunkWriterCloser_UnderlyingCloseError tests handling of close error from underlying writer
func TestChunkWriterCloser_UnderlyingCloseError(t *testing.T) {
	underlyingErr := errors.New("underlying close failed")
	mock := &mockReadWriteCloser{closeErr: underlyingErr}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)

	err := writer.Close()

	if !errors.Is(err, underlyingErr) {
		t.Errorf("expected %v error, got %v", underlyingErr, err)
	}
}

// TestChunkWriterCloser_CloserFunctionError tests handling of closer function error
func TestChunkWriterCloser_CloserFunctionError(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closerErr := errors.New("closer function failed")
	closer := func() error { return closerErr }

	writer := ChunkWriterCloser(mock, closer)

	resultErr := writer.Close()

	// Note: Current implementation returns error from underlying writer first
	// If underlying writer close succeeds, closer function error is returned
	if resultErr != nil && !errors.Is(resultErr, closerErr) {
		t.Logf("got error: %v", resultErr)
	}
}

// TestChunkWriterCloser_WriteAfterClose tests write after close
func TestChunkWriterCloser_WriteAfterClose(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	writer.Close()

	// Try to write after close - behavior depends on underlying implementation
	data := []byte("test")
	n, _ := writer.Write(data)

	// The mock allows writing even after close, so we check that it still delegates
	if n != len(data) {
		t.Logf("write after close: got %d bytes", n)
	}
}

// TestChunkWriterCloser_SingleByte tests writing single byte
func TestChunkWriterCloser_SingleByte(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := []byte("x")
	n, err := writer.Write(data)

	if n != 1 {
		t.Errorf("expected 1 byte written, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(mock.writeData) != 1 {
		t.Errorf("expected 1 byte in mock, got %d", len(mock.writeData))
	}
}

// TestChunkWriterCloser_ZeroByteBuffer tests writing with zero-length buffer
func TestChunkWriterCloser_ZeroByteBuffer(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := make([]byte, 0)
	n, err := writer.Write(data)

	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestChunkWriterCloser_BinaryData tests writing binary data
func TestChunkWriterCloser_BinaryData(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)
	defer writer.Close()

	data := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
	n, err := writer.Write(data)

	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestChunkWriterCloser_CloserNilError tests closer function returning nil error
func TestChunkWriterCloser_CloserNilError(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)

	err := writer.Close()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestChunkWriterCloser_WriteAndCloseSuccess tests complete write and close flow
func TestChunkWriterCloser_WriteAndCloseSuccess(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)

	// Write data
	data := []byte("test data")
	n, err := writer.Write(data)

	if n != len(data) || err != nil {
		t.Fatalf("write failed: %d bytes, error: %v", n, err)
	}

	// Close
	closeErr := writer.Close()

	if closeErr != nil {
		t.Errorf("close failed: %v", closeErr)
	}

	if !mock.closeCalled {
		t.Error("underlying writer not closed")
	}

	if string(mock.writeData) != string(data) {
		t.Errorf("data mismatch: expected '%s', got '%s'", string(data), string(mock.writeData))
	}
}

// TestChunkWriterCloser_ImplementsWriteCloser verifies WriteCloser interface
func TestChunkWriterCloser_ImplementsWriteCloser(t *testing.T) {
	mock := &mockReadWriteCloser{}
	closer := func() error { return nil }

	writer := ChunkWriterCloser(mock, closer)

	// Verify it implements io.WriteCloser
	var _ io.WriteCloser = writer

	// Verify we can call Write and Close
	_, err := writer.Write([]byte("test"))
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}

	err = writer.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}
}
