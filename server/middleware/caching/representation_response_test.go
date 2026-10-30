package caching

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRepresentationResponseRejectsUnprovenRanges(t *testing.T) {
	for _, tc := range []struct {
		name, requestRange, contentRange string
	}{
		{"unsolicited", "", "bytes 0-63/128"},
		{"wrong range", "bytes=64-127", "bytes 0-63/128"},
		{"unknown total", "bytes=0-63", "bytes 0-63/*"},
		{"bad unit", "bytes=0-63", "items 0-63/128"},
		{"multiple ranges", "bytes=0-63,64-127", "bytes 0-63/128"},
		{"invalid bounds", "bytes=0-63", "bytes 0-128/128"},
		{"invalid request start", "bytes=bad-63", "bytes 64-127/128"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := varyRequest(t, "https://example.com/file", http.Header{"Range": {tc.requestRange}})
			resp := &http.Response{StatusCode: 206, ContentLength: 64, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 64))),
				Header: http.Header{"Content-Length": {"64"}, "Content-Range": {tc.contentRange}}}
			if _, _, _, ok := analyzeRepresentationResponse(req, resp, 64); ok {
				t.Fatal("unproven range became a writable representation")
			}
		})
	}
}

func TestRepresentationRangeCroppingConsumesFullOrigin(t *testing.T) {
	for _, tc := range []struct{ raw, want, contentRange string }{
		{"bytes=2-3", "cd", "bytes 2-3/6"},
		{"bytes=4-", "ef", "bytes 4-5/6"},
		{"bytes=-2", "ef", "bytes 4-5/6"},
		{"bytes=-99", "abcdef", "bytes 0-5/6"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			origin := strings.NewReader("abcdef")
			resp := &http.Response{StatusCode: 200, ContentLength: 6, Header: http.Header{"Content-Length": {"6"}}, Body: io.NopCloser(origin)}
			req := varyRequest(t, "https://example.com/file", http.Header{"Range": {tc.raw}})
			resp, err := applyRepresentationRange(req, resp)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(data) != tc.want || origin.Len() != 0 || resp.StatusCode != 206 || resp.Header.Get("Content-Range") != tc.contentRange {
				t.Fatal(resp.Header, string(data), err)
			}
		})
	}
}
