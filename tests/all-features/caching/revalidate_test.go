package caching

import (
	"net/http"
	"testing"
	"time"

	"github.com/omalloc/tavern/pkg/e2e"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/stretchr/testify/assert"
)

func TestRevalidateDiscard(t *testing.T) {
	f := e2e.GenFile(t, 5<<20)

	t.Run("test Revalidate old-file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/revalidate/file1.apk", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "old-file")
			w.Header().Set("Cache-Control", "max-age=1") // 1s 过滤
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(0, 524287)

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

	t.Run("test Revalidate new-file", func(t *testing.T) {
		time.Sleep(time.Second)

		case1 := e2e.New("http://sendya.me.gslb.com/cases/revalidate/file1.apk", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "new-file")
			w.Header().Set("Cache-Control", "max-age=1") // 1s 过滤
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(2097152, 3145727)

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

		assert.Equal(t, "new-file", resp.Header.Get("X-Case"), "response should be X-Case new-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	// t.Run("PURGE", func(t *testing.T) {
	// 	e2e.Purge(t, "http://sendya.me.gslb.com/cases/revalidate/file1.apk")
	// })
}
