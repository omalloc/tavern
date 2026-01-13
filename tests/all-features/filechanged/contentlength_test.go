package filechanged_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/pkg/e2e"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

func TestContengLenChanged(t *testing.T) {
	f := e2e.GenFile(t, 1<<20)
	f2 := e2e.GenFile(t, 2<<20)

	t.Run("test content-length old-file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/cl/file1.apk", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "old-file")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(1, 400000)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, int(rr.Start), int(rr.End))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("test content-length new-file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/cl/file1.apk", e2e.RespCallbackFile(f2, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "new-file")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(600000, 0)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f2.Path, int(rr.Start), int(f.Size-int(rr.Start)))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("test content-length new-file 2", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/cl/file1.apk", e2e.RespCallbackFile(f2, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "new-file")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(600000, 0)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f2.Path, int(rr.Start), int(f2.Size-int(rr.Start)))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f2.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "new-file", resp.Header.Get("X-Case"), "response should be X-Case new-file")

		assert.Equal(t, f2.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/cl/file1.apk")
	})
}

func TestContentLenShorter(t *testing.T) {
	f := e2e.GenFile(t, 1<<20)
	f2 := e2e.GenFile(t, 1048570)

	t.Run("test content-length shorter old file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/cl/shorter.apk", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "old-file")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(1, 400000)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, int(rr.Start), int(rr.RangeLength(int64(f.Size))))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, hashBody, hashFile, "response body should be Conflict MD5")
	})

	t.Run("test content-length shorter new file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/cl/shorter.apk", e2e.RespCallbackFile(f2, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "new-file")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(600000, 0) // bytes=600000-

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		n := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		// Content-Range: bytes 600000-1048569/1048570
		// Content-Range: bytes 600000-1048575/1048575
		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, n, rr.RangeLength(int64(f2.Size)), "response should be Content-Length")
	})

	t.Run("test content-length shorter whole file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/cl/shorter.apk", e2e.RespSimpleFile(f2))
		defer case1.Close()

		rr := xhttp.NewRequestRange(0, 0)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})
		assert.NoError(t, err, "response should not error")

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f2.Path, int(rr.Start), int(rr.RangeLength(int64(f2.Size))))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f2.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "new-file", resp.Header.Get("X-Case"), "response should be X-Case new-file")

		assert.Contains(t, resp.Header.Get("X-Cache"), "PART_MISS", "response should be X-Cache PART_MISS")

		assert.Equal(t, hashBody, hashFile, "response body should be Conflict MD5")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/cl/shorter.apk")
	})
}
