package iobuf_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/omalloc/tavern/pkg/iobuf"
)

// trackedReader wraps an io.Reader and tracks Close calls.
type trackedReader struct {
	reader     io.Reader
	closeCount *int
	closeErr   error
}

func (r *trackedReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *trackedReader) Close() error {
	(*r.closeCount)++
	return r.closeErr
}

// eofCloseErrReader returns data + io.EOF in a single Read, and returns the given error on Close.
type eofCloseErrReader struct {
	data      string
	readOnce  bool
	closeErr  error
	closeCount *int
}

func (r *eofCloseErrReader) Read(p []byte) (int, error) {
	if r.readOnce {
		return 0, io.EOF
	}
	r.readOnce = true
	n := copy(p, r.data)
	if n == 0 {
		return 0, io.EOF
	}
	return n, io.EOF
}

func (r *eofCloseErrReader) Close() error {
	(*r.closeCount)++
	return r.closeErr
}

func TestPartsReader_Read_CloseErrorAdvancesIndex(t *testing.T) {
	closeErr := errors.New("close failed")

	var r1Closes, r2Closes int

	r1 := &eofCloseErrReader{
		data:       "hello",
		closeCount: &r1Closes,
	}
	r2 := &eofCloseErrReader{
		data:       "world",
		closeErr:   closeErr,
		closeCount: &r2Closes,
	}

	pr := iobuf.PartsReadCloser(nil, r1, r2)

	buf := make([]byte, 1024)

	// Read r1: returns "hello" + EOF → close r1, advance to r2, err=nil
	_, err := pr.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error on first read: %v", err)
	}

	// Read r2: returns "world" + EOF → close r2 returns closeErr
	_, err = pr.Read(buf)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected closeErr, got %v", err)
	}

	if r1Closes != 1 {
		t.Errorf("r1 was closed %d times, expected 1", r1Closes)
	}

	// Now Close the outer reader — should NOT close r2 again (r.index was incremented after Close failure)
	_ = pr.Close()

	if r2Closes != 1 {
		t.Errorf("r2 was closed %d times (expected 1) — double-close detected", r2Closes)
	}
}

func TestPartsReader_WriteTo_ErrorAdvancesIndex(t *testing.T) {
	readErr := errors.New("read error")

	var r1Closes, r2Closes int

	r1 := &trackedReader{
		reader:     strings.NewReader("hello"),
		closeCount: &r1Closes,
	}
	r2 := &trackedReader{
		reader:     &errReader{err: readErr},
		closeCount: &r2Closes,
	}

	pr := iobuf.PartsReadCloser(nil, r1, r2)
	wt, ok := pr.(io.WriterTo)
	if !ok {
		t.Fatal("PartsReadCloser does not implement io.WriterTo")
	}

	var buf bytes.Buffer
	n, err := wt.WriteTo(&buf)

	if !errors.Is(err, readErr) {
		t.Fatalf("expected readErr, got %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}
	if r2Closes != 1 {
		t.Errorf("r2 was closed %d times on error, expected 1", r2Closes)
	}

	// Now Close the outer reader — should NOT close r2 again
	_ = pr.Close()

	if r2Closes != 1 {
		t.Errorf("r2 was closed %d times after outer Close (expected 1) — double-close detected", r2Closes)
	}
}

func TestPartsReader_WriteTo_AllSuccess(t *testing.T) {
	var r1Closes, r2Closes int

	r1 := &trackedReader{
		reader:     strings.NewReader("hello"),
		closeCount: &r1Closes,
	}
	r2 := &trackedReader{
		reader:     strings.NewReader("world"),
		closeCount: &r2Closes,
	}

	pr := iobuf.PartsReadCloser(nil, r1, r2)
	wt := pr.(io.WriterTo)

	var buf bytes.Buffer
	n, err := wt.WriteTo(&buf)

	if err != io.EOF {
		t.Fatalf("expected io.EOF after consuming all parts, got %v", err)
	}
	if n != 10 {
		t.Fatalf("expected 10 bytes written, got %d", n)
	}
	if buf.String() != "helloworld" {
		t.Fatalf("expected 'helloworld', got %q", buf.String())
	}
}
