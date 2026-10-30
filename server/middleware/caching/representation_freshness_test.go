package caching

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
)

func TestRepresentationFreshness(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		name       string
		header     http.Header
		dateOffset int64
		delay      int64
		remaining  int64
		age        int64
		bypass     bool
	}{
		{"aged origin", http.Header{"Cache-Control": {"max-age=600"}, "Age": {"120"}}, 0, 0, 480, 120, false},
		{"old date", http.Header{"Cache-Control": {"max-age=600"}, "Age": {"10"}}, -200, 0, 400, 200, false},
		{"request delay", http.Header{"Cache-Control": {"max-age=600"}, "Age": {"120"}}, 0, 3, 477, 123, false},
		{"s maxage", http.Header{"Cache-Control": {"max-age=10,s-maxage=100"}, "Age": {"20"}}, 0, 0, 80, 20, false},
		{"stale origin", http.Header{"Cache-Control": {"max-age=100"}, "Age": {"120"}}, 0, 0, 0, 120, false},
		{"expires with injected clock", http.Header{"Expires": {now.Add(100 * time.Second).UTC().Format(http.TimeFormat)}, "Age": {"20"}}, 0, 0, 80, 20, false},
		{"no store beats override", http.Header{"Cache-Control": {"max-age=600", "no-store"}, protocol.ProtocolCacheTime: {"1000"}}, 0, 0, 0, 0, true},
		{"private beats override", http.Header{"Cache-Control": {"max-age=600,private"}, protocol.ProtocolCacheTime: {"1000"}}, 0, 0, 0, 0, true},
		{"malformed age", http.Header{"Age": {"bad"}}, 0, 0, 0, 0, true},
		{"negative age", http.Header{"Age": {"-1"}}, 0, 0, 0, 0, true},
		{"age saturation", http.Header{"Age": {"9223372036854775807"}}, 0, 2, 0, 9223372036854775807, false},
		{"invalid max age", http.Header{"Cache-Control": {"max-age=bad"}}, 0, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.header.Clone()
			h.Set("Date", now.Add(time.Duration(tc.dateOffset)*time.Second).UTC().Format(http.TimeFormat))
			expires, err := representationFreshness(h, now.Add(-time.Duration(tc.delay)*time.Second), now)
			if tc.bypass {
				if err == nil {
					t.Fatal("policy bypass was admitted")
				}
				return
			}
			if err != nil || expires != now.Unix()+tc.remaining || h.Get("Age") != strconv.FormatInt(tc.age, 10) {
				t.Fatalf("expires %d, age %q, err %v", expires, h.Get("Age"), err)
			}
		})
	}
}

func TestValidationCannotRelabelOldWireBytes(t *testing.T) {
	prior := &object.Metadata{Size: 3, BlockSize: 64, Headers: http.Header{"Content-Type": {"image/jpeg"}, "Content-Encoding": {"gzip"}}}
	prior.Chunks.Set(0)
	req := varyRequest(t, "https://example.com/a", http.Header{"If-None-Match": {`"A"`}})
	for _, header := range []http.Header{{"Content-Encoding": {"identity"}}, {"Content-Type": {"image/png"}}, {"Content-Encoding": {"gzip", "identity"}}} {
		if validValidation(prior, &http.Response{Header: header}, req) {
			t.Fatal("304 relabeled old bytes", header)
		}
	}
	if !validValidation(prior, &http.Response{Header: http.Header{"Content-Encoding": {"gzip"}, "Content-Type": {"image/jpeg"}}}, req) {
		t.Fatal("unchanged coding/type rejected")
	}
}
