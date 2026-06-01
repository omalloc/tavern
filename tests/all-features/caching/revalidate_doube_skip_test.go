package caching

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/pkg/e2e"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

// RevalidateDoubleSkip 测试在 Revalidate 过程中，对已缓存的文件再次发起请求，上游相应 304 Not Modified，内部因为 fillRange 补块导致文件偏移错误
//
//	@see https://github.com/omalloc/tavern/issues/58
func TestRevalidateDoubleSkip(t *testing.T) {
	f := e2e.GenFile(t, 5<<20)

	t.Run("test Revalidate normal old-file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/revalidate-skip/file1.apk", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "old-file")
			w.Header().Set("Cache-Control", "max-age=1") // 1s 过滤
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
		})

		t.Logf("resp size=%d", resp.ContentLength)
		t.Logf("file path=%s", f.Path)

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, 0, f.Size)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusOK, resp.StatusCode, "response should be code 200")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("test Revalidate skip old-file", func(t *testing.T) {
		time.Sleep(1 * time.Second) // 等待上一个 case 的 max-age 过期

		case1 := e2e.New("http://sendya.me.gslb.com/cases/revalidate-skip/file1.apk", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range r.Header {
				t.Logf("header key=%s, value=%s", k, v)
			}
			since := r.Header.Get("If-None-Match")
			t.Logf("If-None-Match=%s", since)

			if since != f.MD5 {
				w.Header().Set("X-Case", "old-file")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusNotModified)
			w.Header().Set("X-Case", "old-file")
			w.Header().Set("Cache-Control", "max-age=1") // 1s 过滤
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(4341324, 4417533)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
			t.Logf("New Request Range=%s", rr.String())
		})

		t.Logf("resp size=%d", resp.ContentLength)
		t.Logf("file offset=%d, end=%d", rr.Start, rr.End)
		t.Logf("file path=%s", f.Path)

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, int(rr.Start), int(rr.End-rr.Start+1))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/revalidate-skip/file1.apk")
	})

}
