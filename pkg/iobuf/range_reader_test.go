package iobuf

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRangeReader_FullRead(t *testing.T) {
	data := "Hello, World!"
	reader := io.NopCloser(strings.NewReader(data))

	// Read bytes 7-11 ("World")
	rr := RangeReader(reader, 0, len(data)-1, 7, 11)

	p := make([]byte, 100)
	n, err := rr.Read(p)

	if n != 5 {
		t.Errorf("expected 5 bytes, got %d", n)
	}
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
	if string(p[:n]) != "World" {
		t.Errorf("expected 'World', got '%s'", string(p[:n]))
	}
}

func TestRangeReader_ReadFromStart(t *testing.T) {
	data := "Hello, World!"
	reader := io.NopCloser(strings.NewReader(data))

	// Read from start: bytes 0-4 ("Hello")
	rr := RangeReader(reader, 0, len(data)-1, 0, 4)

	p := make([]byte, 100)
	n, err := rr.Read(p)

	if n != 5 {
		t.Errorf("expected 5 bytes, got %d", n)
	}
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
	if string(p[:n]) != "Hello" {
		t.Errorf("expected 'Hello', got '%s'", string(p[:n]))
	}
}

func TestRangeReader_MultipleReads(t *testing.T) {
	data := "0123456789"
	reader := io.NopCloser(strings.NewReader(data))

	// Read bytes 3-7 ("34567")
	rr := RangeReader(reader, 0, len(data)-1, 3, 7)

	// First read: 3 bytes
	p1 := make([]byte, 3)
	n1, err1 := rr.Read(p1)

	if n1 != 3 || err1 != nil {
		t.Errorf("first read: expected (3, nil), got (%d, %v)", n1, err1)
	}
	if string(p1[:n1]) != "345" {
		t.Errorf("expected '345', got '%s'", string(p1[:n1]))
	}

	// Second read: remaining 2 bytes should come back with EOF
	p2 := make([]byte, 10)
	n2, err2 := rr.Read(p2)

	if n2 != 2 || err2 != io.EOF {
		t.Errorf("second read: expected (2, io.EOF), got (%d, %v)", n2, err2)
	}
	if string(p2[:n2]) != "67" {
		t.Errorf("expected '67', got '%s'", string(p2[:n2]))
	}
}

func TestRangeReader_EntireRange(t *testing.T) {
	data := "ABCDEFGHIJ"
	reader := io.NopCloser(strings.NewReader(data))

	// Read entire range
	rr := RangeReader(reader, 0, len(data)-1, 0, len(data)-1)

	var buf bytes.Buffer
	n, err := io.Copy(&buf, rr)

	if int(n) != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != data {
		t.Errorf("expected '%s', got '%s'", data, buf.String())
	}
}

func TestRangeReader_SingleByte(t *testing.T) {
	data := "XYZ"
	reader := io.NopCloser(strings.NewReader(data))

	// Read only byte at index 1 ('Y')
	rr := RangeReader(reader, 0, len(data)-1, 1, 1)

	p := make([]byte, 1)
	n, err := rr.Read(p)

	if n != 1 || err != io.EOF {
		t.Errorf("expected (1, io.EOF), got (%d, %v)", n, err)
	}
	if string(p) != "Y" {
		t.Errorf("expected 'Y', got '%s'", string(p))
	}
}

func TestRangeReader_SmallBuffer(t *testing.T) {
	data := "abcdefghij"
	reader := io.NopCloser(strings.NewReader(data))

	// Read range [2, 7] = "cdefgh" (6 bytes) with 2-byte buffer
	rr := RangeReader(reader, 0, len(data)-1, 2, 7)

	result := ""
	p := make([]byte, 2)
	for {
		n, err := rr.Read(p)
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

	if result != "cdefgh" {
		t.Errorf("expected 'cdefgh', got '%s'", result)
	}
}

func TestRangeReader_Close(t *testing.T) {
	closeCalled := false
	mock := &mockCloseTracker{
		Reader: strings.NewReader("test data"),
		onClose: func() { closeCalled = true },
	}

	rr := RangeReader(mock, 0, 8, 0, 8)
	rr.Close()

	if !closeCalled {
		t.Error("expected underlying reader to be closed")
	}
}

func TestRangeReader_WriteToViaCopy(t *testing.T) {
	data := "Hello, World! This is a test."
	reader := io.NopCloser(strings.NewReader(data))

	// bytes 7-18 = "World! This " (12 chars including trailing space)
	rr := RangeReader(reader, 0, len(data)-1, 7, 18)

	var buf bytes.Buffer
	n, err := io.Copy(&buf, rr)

	if n != 12 {
		t.Errorf("expected 12 bytes written, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != "World! This " {
		t.Errorf("expected 'World! This ', got '%s'", buf.String())
	}
}

func TestRangeReader_LargeSkip(t *testing.T) {
	data := strings.Repeat("x", 10000)
	reader := io.NopCloser(strings.NewReader(data))

	// Read range starting far into the data
	rr := RangeReader(reader, 0, len(data)-1, 5000, 5004)

	p := make([]byte, 100)
	n, err := rr.Read(p)

	if n != 5 || err != io.EOF {
		t.Errorf("expected (5, io.EOF), got (%d, %v)", n, err)
	}
	if string(p[:n]) != "xxxxx" {
		t.Errorf("expected 'xxxxx', got '%s'", string(p[:n]))
	}
}

func TestRangeReader_EmptyRange(t *testing.T) {
	data := "test"
	reader := io.NopCloser(strings.NewReader(data))

	// rawStart > rawEnd: degenerate case
	// The implementation will skip to rawStart (4), then Read returns EOF
	rr := RangeReader(reader, 0, 3, 4, 3)

	p := make([]byte, 10)
	n, err := rr.Read(p)

	if n != 0 || err == nil {
		t.Errorf("expected 0 bytes and an error, got (%d, %v)", n, err)
	}
}

func TestRangeReader_DataOvershoot(t *testing.T) {
	// range extends beyond actual data
	data := "short"
	reader := io.NopCloser(strings.NewReader(data))

	// Ask for bytes [2, 20] on a 5-byte string
	rr := RangeReader(reader, 0, 100, 2, 20)

	var buf bytes.Buffer
	n, err := io.Copy(&buf, rr)

	if n != 3 {
		t.Errorf("expected 3 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != "ort" {
		t.Errorf("expected 'ort', got '%s'", buf.String())
	}
}

// mockCloseTracker is a helper that wraps a Reader and tracks Close calls
type mockCloseTracker struct {
	*strings.Reader
	onClose func()
}

func (m *mockCloseTracker) Close() error {
	if m.onClose != nil {
		m.onClose()
	}
	return nil
}
