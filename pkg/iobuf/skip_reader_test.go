package iobuf

import (
	"io"
	"strings"
	"testing"
)

func TestSkipReadCloser_Basic(t *testing.T) {
	data := "Hello, World!"
	reader := io.NopCloser(strings.NewReader(data))

	sr := SkipReadCloser(reader, 7)

	p := make([]byte, 100)
	n, err := sr.Read(p)

	if n != 6 {
		t.Errorf("expected 6 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if string(p[:n]) != "World!" {
		t.Errorf("expected 'World!', got '%s'", string(p[:n]))
	}
}

func TestSkipReadCloser_ZeroSkip(t *testing.T) {
	data := "test"
	reader := io.NopCloser(strings.NewReader(data))

	sr := SkipReadCloser(reader, 0)

	p := make([]byte, 10)
	n, err := sr.Read(p)

	if n != 4 || err != nil {
		t.Errorf("expected (4, nil), got (%d, %v)", n, err)
	}
	if string(p[:n]) != "test" {
		t.Errorf("expected 'test', got '%s'", string(p[:n]))
	}
}

func TestSkipReadCloser_SkipAll(t *testing.T) {
	data := "short"
	reader := io.NopCloser(strings.NewReader(data))

	sr := SkipReadCloser(reader, int64(len(data)))

	p := make([]byte, 10)
	n, err := sr.Read(p)

	if n != 0 || err != io.EOF {
		t.Errorf("expected (0, io.EOF), got (%d, %v)", n, err)
	}
}

func TestSkipReadCloser_SkipBeyond(t *testing.T) {
	data := "abc"
	reader := io.NopCloser(strings.NewReader(data))

	// Skip more bytes than available
	sr := SkipReadCloser(reader, 10)

	p := make([]byte, 10)
	n, err := sr.Read(p)

	if n != 0 || err != io.EOF {
		t.Errorf("expected (0, io.EOF), got (%d, %v)", n, err)
	}
}

func TestSkipReadCloser_MultipleReads(t *testing.T) {
	data := "abcdefghij"
	reader := io.NopCloser(strings.NewReader(data))

	sr := SkipReadCloser(reader, 5)

	// First read
	p1 := make([]byte, 3)
	n1, err1 := sr.Read(p1)
	if n1 != 3 || err1 != nil {
		t.Errorf("first read: expected (3, nil), got (%d, %v)", n1, err1)
	}
	if string(p1) != "fgh" {
		t.Errorf("expected 'fgh', got '%s'", string(p1))
	}

	// Second read
	p2 := make([]byte, 10)
	n2, err2 := sr.Read(p2)
	if n2 != 2 || err2 != nil {
		t.Errorf("second read: expected (2, nil), got (%d, %v)", n2, err2)
	}
	if string(p2[:n2]) != "ij" {
		t.Errorf("expected 'ij', got '%s'", string(p2[:n2]))
	}
}

func TestSkipReadCloser_SmallBuffer(t *testing.T) {
	data := "0123456789"
	reader := io.NopCloser(strings.NewReader(data))

	sr := SkipReadCloser(reader, 4)

	result := ""
	p := make([]byte, 1)
	for {
		n, err := sr.Read(p)
		if n > 0 {
			result += string(p[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if result != "456789" {
		t.Errorf("expected '456789', got '%s'", result)
	}
}

func TestSkipReadCloser_Close(t *testing.T) {
	closeCalled := false
	mock := &mockCloseTracker{
		Reader: strings.NewReader("data"),
		onClose: func() { closeCalled = true },
	}

	sr := SkipReadCloser(mock, 2)
	sr.Close()

	if !closeCalled {
		t.Error("expected close to be called on underlying reader")
	}
}

func TestSkipReadCloser_CloseDelegation(t *testing.T) {
	// Verify that Close works correctly via the wrapper
	reader := io.NopCloser(strings.NewReader("test"))
	sr := SkipReadCloser(reader, 0)

	// Read first
	p := make([]byte, 4)
	sr.Read(p)

	err := sr.Close()
	if err != nil {
		t.Errorf("expected no error on close, got %v", err)
	}
}

func TestSkipReadCloser_ReadAfterSkipThenEOF(t *testing.T) {
	data := "x"
	reader := io.NopCloser(strings.NewReader(data))

	// Skip 1 byte (the only byte), then read should give EOF
	sr := SkipReadCloser(reader, 1)

	p := make([]byte, 1)
	n, err := sr.Read(p)

	if n != 0 || err != io.EOF {
		t.Errorf("expected (0, io.EOF), got (%d, %v)", n, err)
	}
}
