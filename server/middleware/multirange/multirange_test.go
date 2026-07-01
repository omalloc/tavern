package multirange

import (
	"io"
	"net/http"
	"testing"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/rangecontrol"
)

type recordingRoundTripper struct {
	calls       int
	rangeHeader string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	r.rangeHeader = req.Header.Get("Range")
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     make(http.Header),
		Body:       io.NopCloser(http.NoBody),
	}, nil
}

func newTestRoundTripper(t *testing.T, origin http.RoundTripper) http.RoundTripper {
	t.Helper()

	mw, cleanup, err := Middleware(&configv1.Middleware{})
	if err != nil {
		t.Fatalf("Middleware() error = %v", err)
	}
	t.Cleanup(cleanup)

	return mw(origin)
}

func TestMiddlewarePassesSingleSuffixRangeThrough(t *testing.T) {
	tests := []string{
		"bytes=-512",
		"bytes= -512",
	}

	for _, rawRange := range tests {
		t.Run(rawRange, func(t *testing.T) {
			origin := &recordingRoundTripper{}
			rt := newTestRoundTripper(t, origin)

			req, err := http.NewRequest(http.MethodGet, "http://example.com/file", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Range", rawRange)

			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip() error = %v", err)
			}
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			if origin.calls != 1 {
				t.Fatalf("origin calls = %d, want 1", origin.calls)
			}
			if origin.rangeHeader != rawRange {
				t.Fatalf("origin Range = %q, want %q", origin.rangeHeader, rawRange)
			}
		})
	}
}

func TestMiddlewarePassesSingleRangeThrough(t *testing.T) {
	origin := &recordingRoundTripper{}
	rt := newTestRoundTripper(t, origin)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-499")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	if origin.calls != 1 {
		t.Fatalf("origin calls = %d, want 1", origin.calls)
	}
}

func TestMiddlewareRejectsInvalidRange(t *testing.T) {
	tests := []string{
		"bytes=abc",
		"bytes=-abc",
		"bytes=-",
	}

	for _, rawRange := range tests {
		t.Run(rawRange, func(t *testing.T) {
			origin := &recordingRoundTripper{}
			rt := newTestRoundTripper(t, origin)

			req, err := http.NewRequest(http.MethodGet, "http://example.com/file", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Range", rawRange)

			resp, err := rt.RoundTrip(req)
			if err == nil {
				t.Fatal("RoundTrip() error = nil, want error")
			}
			if resp != nil {
				t.Fatalf("RoundTrip() response = %#v, want nil", resp)
			}
			if origin.calls != 0 {
				t.Fatalf("origin calls = %d, want 0", origin.calls)
			}
			bizErr, ok := xhttp.ParseBizError(err)
			if !ok {
				t.Fatalf("error type = %T, want xhttp.BizError", err)
			}
			if bizErr.Code() != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("error code = %d, want %d", bizErr.Code(), http.StatusRequestedRangeNotSatisfiable)
			}
		})
	}
}

func TestMiddlewareDoesNotPassMixedSuffixMultiRangeThrough(t *testing.T) {
	origin := &recordingRoundTripper{}
	rt := newTestRoundTripper(t, origin)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-99,-512")

	resp, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("RoundTrip() error = nil, want error")
	}
	if resp != nil {
		t.Fatalf("RoundTrip() response = %#v, want nil", resp)
	}
	if origin.calls != 0 {
		t.Fatalf("origin calls = %d, want 0", origin.calls)
	}
	bizErr, ok := xhttp.ParseBizError(err)
	if !ok {
		t.Fatalf("error type = %T, want xhttp.BizError", err)
	}
	if bizErr.Code() != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("error code = %d, want %d", bizErr.Code(), http.StatusRequestedRangeNotSatisfiable)
	}
}

func TestMultirangeParse(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    []rangecontrol.ByteRange
		wantErr bool
	}{
		{name: "multi-range", header: "bytes=0-499,-500", want: nil, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			partCount, hasSuffixRange := hasSuffixRange(tc.header)
			t.Logf("suffix-range = %v, part-count = %d", hasSuffixRange, partCount)
		})
	}
}
