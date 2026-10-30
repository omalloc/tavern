package caching

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepresentationAmbiguousValidatorForcesUnconditionalFetch(t *testing.T) {
	for _, field := range []string{"ETag", "Last-Modified"} {
		t.Run(field, func(t *testing.T) {
			var calls atomic.Int32
			value := `"old"`
			condition := "If-None-Match"
			if field == "Last-Modified" {
				value = time.Unix(1700000000, 0).UTC().Format(http.TimeFormat)
				condition = "If-Modified-Since"
			}
			f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch calls.Add(1) {
				case 1:
					w.Header().Set(field, value)
					originImage(w, "old", "", "")
				case 2:
					if r.Header.Get(condition) != value {
						t.Error("old representation was not validated")
					}
					w.Header().Add(field, value)
					w.Header().Add(field, "ambiguous")
					w.WriteHeader(http.StatusNotModified)
				default:
					if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
						t.Error("retry retained ambiguous validation conditions")
					}
					originImage(w, "new", "", `"new"`)
				}
			})
			f.get(t, nil)
			f.clock.Add(601)
			_, data := f.get(t, nil)
			if data != "new" || calls.Load() != 3 {
				t.Fatal("ambiguous validator refreshed old bytes", data, calls.Load())
			}
			_, data = f.get(t, nil)
			if data != "new" || calls.Load() != 3 {
				t.Fatal("unconditional replacement was not reusable", data, calls.Load())
			}
		})
	}
}
