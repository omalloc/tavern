package iobuf

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRateLimitReader_Basic(t *testing.T) {
	data := "Hello, World!"
	reader := io.NopCloser(strings.NewReader(data))

	// 1000 Kbps ≈ 125 KB/s — fast enough that this small data won't hit the limit
	rr := NewRateLimitReader(reader, 1000)

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

func TestRateLimitReader_Empty(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(""))
	rr := NewRateLimitReader(reader, 1000)

	p := make([]byte, 10)
	n, err := rr.Read(p)

	if n != 0 || err != io.EOF {
		t.Errorf("expected (0, io.EOF), got (%d, %v)", n, err)
	}
}

func TestRateLimitReader_SmallBuffer(t *testing.T) {
	data := "abcdefgh"
	reader := io.NopCloser(strings.NewReader(data))

	// High rate to avoid waiting
	rr := NewRateLimitReader(reader, 100000)

	p := make([]byte, 3)
	n1, _ := rr.Read(p)
	if n1 != 3 || string(p) != "abc" {
		t.Errorf("first read: expected 'abc', got '%s'", string(p[:n1]))
	}

	p2 := make([]byte, 3)
	n2, _ := rr.Read(p2)
	if n2 != 3 || string(p2) != "def" {
		t.Errorf("second read: expected 'def', got '%s'", string(p2[:n2]))
	}
}

func TestRateLimitReader_LargeBuffer(t *testing.T) {
	data := "short"
	reader := io.NopCloser(strings.NewReader(data))

	rr := NewRateLimitReader(reader, 100000)

	p := make([]byte, 100)
	n, err := rr.Read(p)

	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}
	// The reader may return io.EOF along with the final bytes, which is valid.
	if err != nil && err != io.EOF {
		t.Errorf("expected nil or io.EOF, got %v", err)
	}
}

func TestRateLimitReader_Close(t *testing.T) {
	closeCalled := false
	mock := &mockCloseTracker{
		Reader: strings.NewReader("data"),
		onClose: func() { closeCalled = true },
	}

	rr := NewRateLimitReader(mock, 1000)
	rr.Close()

	if !closeCalled {
		t.Error("expected underlying reader to be closed")
	}
}

func TestRateLimitReader_Throttling(t *testing.T) {
	// rateLimitReader sets burst = rate = Kbps << 10 bytes/s.
	// For Kbps=10: rate=10240 tokens/s, burst=10240.
	// Each Read loop iteration reads at most burst bytes.
	// With 30 KB of data and a 32 KB io.Copy buffer, we get:
	//   read1: 10240 bytes (instant, uses burst)
	//   read2: 10240 bytes (waits ~1s for burst refill)
	//   read3: 10240 bytes (waits ~1s)
	//   read4:   0,    io.EOF
	// Total: ≥2 seconds of waiting.
	data := strings.Repeat("x", 30*1024)
	reader := io.NopCloser(strings.NewReader(data))

	start := time.Now()
	rr := NewRateLimitReader(reader, 10)

	var buf bytes.Buffer
	_, err := io.Copy(&buf, rr)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.Len() != 30*1024 {
		t.Errorf("expected %d bytes, got %d", 30*1024, buf.Len())
	}

	// Should take at least 1.5s due to token-bucket throttling.
	if elapsed < 1500*time.Millisecond {
		t.Errorf("expected throttling to take >= 1.5s, took %v", elapsed)
	}

	t.Logf("30 KB at 10 Kbps (burst=10 KB) took %v", elapsed)
}

func TestRateLimitReader_Copy(t *testing.T) {
	data := "Hello, World!"
	reader := io.NopCloser(strings.NewReader(data))

	rr := NewRateLimitReader(reader, 100000)

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

func TestRateLimitReader_ZeroRate(t *testing.T) {
	data := "test"
	reader := io.NopCloser(strings.NewReader(data))

	// 0 Kbps — tokens will never arrive
	rr := NewRateLimitReader(reader, 0)

	done := make(chan struct{})
	go func() {
		p := make([]byte, 10)
		rr.Read(p)
		close(done)
	}()

	select {
	case <-done:
		t.Error("Read completed with 0 Kbps rate — expected to block indefinitely")
	case <-time.After(200 * time.Millisecond):
		// Expected: reader blocks waiting for tokens
	}
}
