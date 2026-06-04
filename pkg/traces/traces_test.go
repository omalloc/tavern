package traces

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestWithTrace_GeneratesRequestID(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	req, tr := WithTrace(req)

	if tr.RequestID == "" {
		t.Error("expected generated RequestID, got empty string")
	}
	if len(tr.RequestID) != 32 {
		t.Errorf("expected 32-char hex string, got %d chars: %q", len(tr.RequestID), tr.RequestID)
	}
	if got := FromContext(req.Context()).RequestID; got != tr.RequestID {
		t.Errorf("context RequestID = %q, want %q", got, tr.RequestID)
	}
}

func TestWithTrace_ReusesHeaderRequestID(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	const want = "my-custom-request-id"
	req.Header.Set("X-Request-ID", want)

	req, tr := WithTrace(req)
	if tr.RequestID != want {
		t.Errorf("RequestID = %q, want %q", tr.RequestID, want)
	}
}

func TestWithTrace_SetsStartAt(t *testing.T) {
	before := time.Now()
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	req, tr := WithTrace(req)
	after := time.Now()

	if tr.StartAt.Before(before) || tr.StartAt.After(after) {
		t.Errorf("StartAt = %v, want between %v and %v", tr.StartAt, before, after)
	}
}

func TestWithTrace_ReturnsSamePointer(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	req, tr := WithTrace(req)

	if got := FromContext(req.Context()); got != tr {
		t.Error("pointer from context differs from WithTrace return")
	}
}

func TestFromContext_NoTrace(t *testing.T) {
	ctx := context.Background()
	tr := FromContext(ctx)

	if tr == nil {
		t.Fatal("FromContext returned nil, want zero-value Trace")
	}
	if tr.RequestID != "" {
		t.Errorf("RequestID = %q, want empty", tr.RequestID)
	}
	if !tr.StartAt.IsZero() {
		t.Errorf("StartAt = %v, want zero", tr.StartAt)
	}
}

func TestFromContext_HasTrace(t *testing.T) {
	ctx := context.Background()
	want := &Trace{RequestID: "abc123", CacheStatus: "HIT"}
	ctx = NewContext(ctx, want)

	got := FromContext(ctx)
	if got != want {
		t.Fatal("FromContext returned different pointer than stored")
	}
	if got.CacheStatus != "HIT" {
		t.Errorf("CacheStatus = %q, want HIT", got.CacheStatus)
	}
}

func TestNewContext(t *testing.T) {
	ctx := context.Background()
	tr := &Trace{RequestID: "test-id"}
	ctx = NewContext(ctx, tr)

	got := FromContext(ctx)
	if got != tr {
		t.Error("NewContext did not store the Trace pointer")
	}
}

func TestClone_ValuesEqual(t *testing.T) {
	orig := &Trace{
		RequestID:   "req-1",
		CacheStatus: "MISS",
		RecvReq:     1024,
		SentResp:    512,
	}
	cloned := orig.Clone()

	if cloned.RequestID != orig.RequestID {
		t.Errorf("RequestID = %q, want %q", cloned.RequestID, orig.RequestID)
	}
	if cloned.CacheStatus != orig.CacheStatus {
		t.Errorf("CacheStatus = %q, want %q", cloned.CacheStatus, orig.CacheStatus)
	}
	if cloned.RecvReq != orig.RecvReq {
		t.Errorf("RecvReq = %d, want %d", cloned.RecvReq, orig.RecvReq)
	}
}

func TestClone_IndependentMutation(t *testing.T) {
	orig := &Trace{RequestID: "orig"}
	cloned := orig.Clone()

	cloned.RequestID = "modified"
	cloned.CacheStatus = "HIT"

	if orig.RequestID != "orig" {
		t.Errorf("original RequestID changed to %q", orig.RequestID)
	}
	if orig.CacheStatus != "" {
		t.Errorf("original CacheStatus changed to %q", orig.CacheStatus)
	}
}

func TestClone_IndependentStructFields(t *testing.T) {
	orig := &Trace{StartAt: time.Unix(1000, 0)}
	cloned := orig.Clone()

	cloned.StartAt = time.Unix(2000, 0)

	if !orig.StartAt.Equal(time.Unix(1000, 0)) {
		t.Errorf("original StartAt changed to %v", orig.StartAt)
	}
}

func TestMustParseRequestID_FromHeader(t *testing.T) {
	h := http.Header{}
	h.Set("X-Request-ID", "inbound-id-42")
	got := MustParseRequestID(h)
	if got != "inbound-id-42" {
		t.Errorf("got %q, want inbound-id-42", got)
	}
}

func TestMustParseRequestID_EmptyHeader(t *testing.T) {
	h := http.Header{}
	got := MustParseRequestID(h)
	if got == "" {
		t.Error("expected generated ID, got empty string")
	}
	if len(got) != 32 {
		t.Errorf("expected 32-char hex, got %d: %q", len(got), got)
	}
}

func TestMustParseRequestID_HeaderPresentButEmpty(t *testing.T) {
	h := http.Header{}
	h.Set("X-Request-ID", "")
	got := MustParseRequestID(h)
	if got == "" {
		t.Error("expected fallback generated ID, got empty")
	}
}

func TestRequestID_NilContext(t *testing.T) {
	valuer := RequestID()
	got := valuer(nil)
	if got != "" {
		t.Errorf("got %q, want empty string for nil context", got)
	}
}

func TestRequestID_ValidContext(t *testing.T) {
	ctx := NewContext(context.Background(), &Trace{RequestID: "log-req-id"})
	valuer := RequestID()
	got := valuer(ctx)
	if got != "log-req-id" {
		t.Errorf("got %q, want log-req-id", got)
	}
}

func TestRequestID_NoTraceInContext(t *testing.T) {
	ctx := context.Background()
	valuer := RequestID()
	got := valuer(ctx)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestTrace_ZeroValueFields(t *testing.T) {
	var tr Trace
	if tr.StartAt.IsZero() != true {
		t.Error("zero StartAt should be zero time")
	}
	if tr.RequestID != "" {
		t.Error("zero RequestID should be empty string")
	}
	if tr.RecvReq != 0 {
		t.Error("zero RecvReq should be 0")
	}
	if tr.SentResp != 0 {
		t.Error("zero SentResp should be 0")
	}
}
