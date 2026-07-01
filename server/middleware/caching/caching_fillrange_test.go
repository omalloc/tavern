package caching

import (
	"io"
	"net/http"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
)

func newTestFillRange() *fillRange {
	return NewFillRangeProcessor(
		WithFillRangePercent(100),
		WithChunkSize(256),
	).(*fillRange)
}

func newTestCaching(md *object.Metadata) *Caching {
	return &Caching{
		log: log.NewHelper(log.NewStdLogger(io.Discard)),
		md:  md,
	}
}

func newFillRangeRequest(t *testing.T, rawRange string) *http.Request {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, "http://example.com/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", rawRange)
	return req
}

func TestFillRangeUnknownSizeSuffixRangePassesThrough(t *testing.T) {
	tests := []string{
		"bytes=-512",
		"bytes= -512",
		"bytes=0-99,-512",
		"bytes=0-99, -512",
	}

	for _, rawRange := range tests {
		t.Run(rawRange, func(t *testing.T) {
			f := newTestFillRange()
			req := newFillRangeRequest(t, rawRange)

			got := f.fill(newTestCaching(nil), req, rawRange)
			if got != req {
				t.Fatalf("fill() returned a different request for unknown-size suffix range")
			}
			if got.Header.Get("Range") != rawRange {
				t.Fatalf("Range = %q, want %q", got.Header.Get("Range"), rawRange)
			}
			if got.Context().Value(fillRangeKey{}) != nil {
				t.Fatal("fillRange context present, want nil")
			}
		})
	}
}

func TestFillRangeKnownSizeSuffixRangeIsProcessed(t *testing.T) {
	f := newTestFillRange()
	req := newFillRangeRequest(t, "bytes=-512")

	got := f.fill(newTestCaching(&object.Metadata{Size: 1000}), req, req.Header.Get("Range"))
	if got == req {
		t.Fatal("fill() returned original request, want request with fillRange context")
	}
	if got.Header.Get("Range") == "bytes=-512" {
		t.Fatal("Range was not rewritten for known-size suffix range")
	}

	fill, ok := got.Context().Value(fillRangeKey{}).(*fillRangeContext)
	if !ok {
		t.Fatalf("fillRange context type = %T, want *fillRangeContext", got.Context().Value(fillRangeKey{}))
	}
	if fill.rawStart != 488 || fill.rawEnd != 999 {
		t.Fatalf("raw range = %d-%d, want 488-999", fill.rawStart, fill.rawEnd)
	}
}

func TestFillRangeUnknownSizeRegularRangeStillProcesses(t *testing.T) {
	f := newTestFillRange()
	req := newFillRangeRequest(t, "bytes=0-99")

	got := f.fill(newTestCaching(nil), req, req.Header.Get("Range"))
	if got == req {
		t.Fatal("fill() returned original request, want request with fillRange context")
	}
	if got.Context().Value(fillRangeKey{}) == nil {
		t.Fatal("fillRange context missing")
	}
	if got.Header.Get("Range") != "bytes=0-255" {
		t.Fatalf("Range = %q, want %q", got.Header.Get("Range"), "bytes=0-255")
	}
}
