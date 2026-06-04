package iobuf

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestRateLimitedWriter_Basic(t *testing.T) {
	var buf bytes.Buffer
	w := NewRateLimitedWriter(&buf, 1000000) // 1 MB/s

	data := []byte("Hello, World!")
	n, err := w.Write(data)

	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if buf.String() != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got '%s'", buf.String())
	}
}

func TestRateLimitedWriter_Empty(t *testing.T) {
	var buf bytes.Buffer
	w := NewRateLimitedWriter(&buf, 1000000)

	n, err := w.Write([]byte{})

	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestRateLimitedWriter_MultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	w := NewRateLimitedWriter(&buf, 1000000)

	w.Write([]byte("Hello, "))
	w.Write([]byte("World!"))

	if buf.String() != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got '%s'", buf.String())
	}
}

func TestRateLimitedWriter_LargeWrite(t *testing.T) {
	var buf bytes.Buffer
	w := NewRateLimitedWriter(&buf, 1000000)

	data := bytes.Repeat([]byte("x"), 10000)
	n, err := w.Write(data)

	if n != 10000 {
		t.Errorf("expected 10000 bytes, got %d", n)
	}
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestRateLimitedWriter_Throttling(t *testing.T) {
	// RateLimitedWriter calls WaitN(len(p)) before each Write.
	// Since burst = rate, a single Write must not exceed the burst.
	// Two sequential writes of burst-size each will show throttling:
	// first write is instant, second must wait for tokens to refill.
	var buf bytes.Buffer
	w := NewRateLimitedWriter(&buf, 1000) // rate=1000 B/s, burst=1000 B

	data1 := bytes.Repeat([]byte("a"), 1000)
	data2 := bytes.Repeat([]byte("b"), 1000)

	start := time.Now()

	// First 1000 bytes: instant (uses initial burst)
	_, err := w.Write(data1)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Second 1000 bytes: must wait ~1s for burst to refill
	_, err = w.Write(data2)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	elapsed := time.Since(start)

	if buf.Len() != 2000 {
		t.Errorf("expected 2000 bytes, got %d", buf.Len())
	}

	// Should take at least 500ms due to token-bucket throttling on second write.
	if elapsed < 500*time.Millisecond {
		t.Errorf("expected throttling to take >= 0.5s, took %v", elapsed)
	}

	t.Logf("2×1000 bytes at 1000 B/s (burst=1000) took %v", elapsed)
}

func TestCopyWithRateLimit_Basic(t *testing.T) {
	data := "Hello, World!"
	src := strings.NewReader(data)

	var dst bytes.Buffer
	limiter := rate.NewLimiter(rate.Inf, 0) // no limit

	err := CopyWithRateLimit(&dst, src, limiter)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if dst.String() != data {
		t.Errorf("expected '%s', got '%s'", data, dst.String())
	}
}

func TestCopyWithRateLimit_LargeData(t *testing.T) {
	data := strings.Repeat("x", 100000)
	src := strings.NewReader(data)

	var dst bytes.Buffer
	limiter := rate.NewLimiter(rate.Inf, 0)

	err := CopyWithRateLimit(&dst, src, limiter)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(dst.Bytes()) != 100000 {
		t.Errorf("expected 100000 bytes, got %d", len(dst.Bytes()))
	}
}

func TestCopyWithRateLimit_Empty(t *testing.T) {
	src := strings.NewReader("")

	var dst bytes.Buffer
	limiter := rate.NewLimiter(rate.Inf, 0)

	err := CopyWithRateLimit(&dst, src, limiter)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if dst.Len() != 0 {
		t.Errorf("expected empty dst, got %d bytes", dst.Len())
	}
}

func TestCopyWithRateLimit_Throttling(t *testing.T) {
	// CopyWithRateLimit uses a 32 KB internal buffer and calls WaitN for each
	// chunk. The limiter's burst must be >= 32 KB for WaitN to succeed.
	//
	// rate=32 KB/s, burst=64 KB, data=128 KB:
	//   read1: 32 KB — instant (burst 64K→32K)
	//   read2: 32 KB — instant (burst 32K→0)
	//   read3: 32 KB — waits ~1s for 32K tokens
	//   read4: 32 KB — waits ~1s
	// Total: ≥ 1.5s.
	data := strings.Repeat("x", 128*1024)
	src := strings.NewReader(data)

	var dst bytes.Buffer
	limiter := rate.NewLimiter(32*1024, 64*1024) // 32 KB/s, burst=64 KB

	start := time.Now()
	err := CopyWithRateLimit(&dst, src, limiter)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if dst.Len() != 128*1024 {
		t.Errorf("expected %d bytes, got %d", 128*1024, dst.Len())
	}

	if elapsed < 1*time.Second {
		t.Errorf("expected throttling to take >= 1s, took %v", elapsed)
	}

	t.Logf("128 KB at 32 KB/s (burst=64 KB) took %v", elapsed)
}

func TestCopyWithRateLimit_UnderlyingWriteError(t *testing.T) {
	data := strings.Repeat("x", 1000)
	src := strings.NewReader(data)

	writeErr := io.ErrShortWrite
	dst := &errorWriter{err: writeErr}

	limiter := rate.NewLimiter(rate.Inf, 0)

	err := CopyWithRateLimit(dst, src, limiter)

	if err != writeErr {
		t.Errorf("expected %v, got %v", writeErr, err)
	}
}

// errorWriter is a writer that returns an error on every Write
type errorWriter struct {
	err error
}

func (e *errorWriter) Write(p []byte) (n int, err error) {
	return 0, e.err
}
