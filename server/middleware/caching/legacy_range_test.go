package caching

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestLegacyMissingRangeEstablishesVersionBeforeSendingHeaders(t *testing.T) {
	for _, mode := range []string{"strong", "ignores-if-range", "unquoted", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			old, fresh := bytes.Repeat([]byte{'o'}, 256), bytes.Repeat([]byte{'n'}, 384)
			var calls atomic.Int32
			var changed atomic.Bool
			rt, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				data, tag := old, `"old"`
				if changed.Load() {
					data, tag = fresh, `"new"`
				}
				if mode == "unquoted" {
					tag = tag[1 : len(tag)-1]
				}
				if n == 2 {
					if mode == "unquoted" || mode == "duplicate" {
						if r.Header.Get("Range") != "" {
							t.Error("unproved range was not rebuilt completely")
						}
					} else if r.Header.Get("If-Range") != `"old"` {
						t.Error("missing strong version proof")
					}
				}
				if mode == "ignores-if-range" {
					r.Header.Del("If-Range")
				}
				w.Header().Set("ETag", tag)
				if mode == "duplicate" {
					w.Header().Add("ETag", `"ambiguous"`)
				}
				w.Header().Set("Cache-Control", "max-age=600")
				w.Header().Set("Content-Type", "application/octet-stream")
				http.ServeContent(w, r, "bytes.bin", time.Time{}, bytes.NewReader(data))
			})
			_, data, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Range": {"bytes=0-63"}}))
			if err != nil || data != string(old[:64]) {
				t.Fatal(data, err)
			}
			changed.Store(true)
			response, data, err := readVaryResponse(rt, varyRequest(t, target, http.Header{"Range": {"bytes=128-"}}))
			newTag := `"new"`
			if mode == "unquoted" {
				newTag = "new"
			}
			if err != nil || data != string(fresh[128:]) || response.Header.Get("ETag") != newTag || response.Header.Get("Content-Range") != "bytes 128-383/384" || response.ContentLength != 256 {
				t.Fatalf("old headers or bytes escaped: response=%v bytes=%d err=%v", response, len(data), err)
			}
			wantCalls := int32(2)
			if mode == "ignores-if-range" {
				wantCalls++
			}
			if calls.Load() != wantCalls {
				t.Fatal("version retry was not bounded", calls.Load(), wantCalls)
			}
			_, data, err = readVaryResponse(rt, varyRequest(t, target, http.Header{"Range": {"bytes=0-63"}}))
			if err != nil || data != string(fresh[:64]) || calls.Load() != wantCalls {
				t.Fatal("replacement did not publish only the new complete bytes", err, calls.Load())
			}
		})
	}
}

func TestLegacyMissingRangeCanAccumulateProvenBlocks(t *testing.T) {
	var calls atomic.Int32
	raw := bytes.Repeat([]byte{'x'}, 256)
	rt, _, target := newVaryTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 && r.Header.Get("If-Range") != `"same"` {
			t.Error("incremental fill lacks strong proof")
		}
		w.Header().Set("ETag", `"same"`)
		w.Header().Set("Cache-Control", "max-age=600")
		http.ServeContent(w, r, "bytes.bin", time.Time{}, bytes.NewReader(raw))
	})
	for _, span := range [][2]int{{0, 63}, {64, 127}, {0, 127}} {
		header := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", span[0], span[1])}}
		response, data, err := readVaryResponse(rt, varyRequest(t, target, header))
		if err != nil || len(data) != span[1]-span[0]+1 || response.Header.Get("Content-Length") != strconv.Itoa(len(data)) {
			t.Fatal(len(data), err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("same-generation blocks did not remain reusable", calls.Load())
	}
}
