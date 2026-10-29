package http

import (
	"net/http"
	"testing"
	"time"
)

func TestCacheAdmissionTTL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		control   []string
		override  string
		want      time.Duration
		cacheable bool
	}{
		{"max age", []string{"max-age=600"}, "", 600 * time.Second, true},
		{"no store after max age", []string{"max-age=600,no-store"}, "", 0, false},
		{"private before max age", []string{"private,max-age=600"}, "", 0, false},
		{"qualified private", []string{`max-age=600, private="Set-Cookie"`}, "", 0, false},
		{"multiline", []string{"max-age=600", "no-store"}, "", 0, false},
		{"override cannot permit no store", []string{"no-store"}, "600", 0, false},
		{"override cannot permit private", []string{"private"}, "600", 0, false},
		{"no cache", []string{"max-age=600,no-cache"}, "600", 0, false},
		{"case whitespace quoted age", []string{`MAX-AGE = "600"`}, "", 600 * time.Second, true},
		{"zero age", []string{"max-age=0"}, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{"Cache-Control": tc.control}
			h.Set("X-TTL", tc.override)
			got, cacheable := ParseCacheTime("X-TTL", h)
			if got != tc.want || cacheable != tc.cacheable {
				t.Fatalf("ParseCacheTime = %v, %t; want %v, %t", got, cacheable, tc.want, tc.cacheable)
			}
		})
	}
}
