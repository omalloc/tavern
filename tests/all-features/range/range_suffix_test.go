package range_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/pkg/e2e"
)

const suffixRangeLength = 512

func TestSuffixRangeMissPassesOriginalRange(t *testing.T) {
	f := e2e.GenFile(t, 2<<20)

	tests := []string{
		"bytes=-512",
		"bytes= -512",
	}

	for _, rawRange := range tests {
		t.Run(rawRange, func(t *testing.T) {
			url := fmt.Sprintf("http://sendya.me.gslb.com/cases/range/suffix/miss/%s", uuid.NewString())
			originRanges := make(chan string, 1)
			case1 := e2e.New(url, e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
				originRanges <- r.Header.Get("Range")
			}))
			defer case1.Close()

			resp, err := case1.Do(func(r *http.Request) {
				r.Header.Set("Range", rawRange)
			})
			if !assert.NoError(t, err, "response should not error") {
				return
			}
			if resp == nil {
				t.Fatal("response should not be nil")
			}

			hashBody := e2e.HashBody(resp)
			start, length, contentRange := suffixRangeExpectation(f)
			hashFile := e2e.HashFile(f.Path, start, length)

			assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")
			assert.Contains(t, resp.Header.Get("X-Cache"), "MISS", "cache should be MISS")
			assert.Equal(t, contentRange, resp.Header.Get("Content-Range"), "response should be Content-Range")
			assert.Equal(t, int64(length), resp.ContentLength, "size should be suffix length")
			assert.Equal(t, hashFile, hashBody, "response body should be suffix bytes")

			select {
			case got := <-originRanges:
				assert.Equal(t, rawRange, got, "origin should receive original suffix range")
			default:
				t.Fatal("origin was not called")
			}

			e2e.Purge(t, url)
		})
	}
}

func TestSuffixRangeHitUsesCachedObjectSize(t *testing.T) {
	f := e2e.GenFile(t, 2<<20)
	url := fmt.Sprintf("http://sendya.me.gslb.com/cases/range/suffix/hit/%s", uuid.NewString())

	t.Run("warm full object", func(t *testing.T) {
		case1 := e2e.New(url, e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {})
		if !assert.NoError(t, err, "response should not error") {
			return
		}
		if resp == nil {
			t.Fatal("response should not be nil")
		}

		size := e2e.DiscardBody(resp, 5*1024)

		assert.Equal(t, http.StatusOK, resp.StatusCode, "response should be code 200")
		assert.Contains(t, resp.Header.Get("X-Cache"), "MISS", "cache should be MISS")
		assert.Equal(t, f.Size, size, "size should be file size")
	})

	t.Run("suffix range HIT", func(t *testing.T) {
		case1 := e2e.New(url, e2e.WrongHit(t))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=-512")
		})
		if !assert.NoError(t, err, "response should not error") {
			return
		}
		if resp == nil {
			t.Fatal("response should not be nil")
		}

		hashBody := e2e.HashBody(resp)
		start, length, contentRange := suffixRangeExpectation(f)
		hashFile := e2e.HashFile(f.Path, start, length)

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")
		assert.Contains(t, resp.Header.Get("X-Cache"), "HIT", "cache should be HIT")
		assert.Equal(t, contentRange, resp.Header.Get("Content-Range"), "response should be Content-Range")
		assert.Equal(t, int64(length), resp.ContentLength, "size should be suffix length")
		assert.Equal(t, hashFile, hashBody, "response body should be suffix bytes")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, url)
	})
}

func suffixRangeExpectation(f *e2e.MockFile) (start, length int, contentRange string) {
	length = suffixRangeLength
	if f.Size < length {
		length = f.Size
	}
	start = f.Size - length
	contentRange = fmt.Sprintf("bytes %d-%d/%d", start, f.Size-1, f.Size)
	return start, length, contentRange
}
