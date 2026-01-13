package filechanged_test

import (
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/pkg/e2e"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

func init() {
	e2e.SetLocalAddr("/tmp/tavern.sock")
}

func TestLastModified(t *testing.T) {
	f := e2e.GenFile(t, 1<<20)

	oldlm := time.Now().UTC().Format(http.TimeFormat)
	newlm := time.Now().Add(time.Second * 2).UTC().Format(http.TimeFormat)

	t.Logf("oldlm: %s", oldlm)
	t.Logf("newlm: %s", newlm)

	t.Run("test last-modified old-file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/lm/file1.apk", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "old-file")
			w.Header().Set("Last-Modified", oldlm)
			w.Header().Set("ETag", f.MD5)

			rr, err := xhttp.SingleRange(r.Header.Get("Range"), uint64(f.Size))
			if err != nil {
				t.Fatal(err)
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}

			reader := e2e.SplitFile(f.Path, int(rr.Start), int(rr.Length()))

			w.Header().Set("Content-Length", strconv.Itoa(int(rr.Length())))
			w.Header().Set("Content-Range", rr.ContentRange(uint64(f.Size)))

			w.WriteHeader(http.StatusPartialContent)
			n, err := io.Copy(w, reader)

			assert.NoError(t, err, "copy should not error")

			assert.Equal(t, n, rr.Length(), "copy bytes should be equal to range length")
		}))

		rr := xhttp.NewRequestRange(0, 32767)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, int(rr.Start), int(rr.Length()))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, oldlm, resp.Header.Get("Last-Modified"), "response should be Last-Modified")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("test last-modified new-file", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/lm/file1.apk", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "new-file")
			w.Header().Set("Last-Modified", newlm)
			w.Header().Set("ETag", f.MD5)

			rr, err := xhttp.SingleRange(r.Header.Get("Range"), uint64(f.Size))
			if err != nil {
				t.Fatal(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			reader := e2e.SplitFile(f.Path, int(rr.Start), int(rr.Length()))

			w.Header().Set("Content-Length", strconv.Itoa(int(rr.Length())))
			w.Header().Set("Content-Range", rr.ContentRange(uint64(f.Size)))
			w.WriteHeader(http.StatusPartialContent)

			n, err := io.Copy(w, reader)

			assert.NoError(t, err, "copy file to response should not error")

			assert.Equal(t, n, rr.Length(), "copy bytes should be equal to range length")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(600000, 0)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, int(rr.Start), int(rr.RangeLength(int64(f.Size))))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, int64(f.Size-int(rr.Start)), resp.ContentLength, "response should be Content-Length")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "old-file", resp.Header.Get("X-Case"), "response should be X-Case old-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, oldlm, resp.Header.Get("Last-Modified"), "response should be Last-Modified")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("test last-modified new-file 2", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/lm/file1.apk", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Case", "new-file")
			w.Header().Set("Last-Modified", newlm)
			w.Header().Set("ETag", f.MD5)

			rr, err := xhttp.SingleRange(r.Header.Get("Range"), uint64(f.Size))
			if err != nil {
				t.Fatal(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			reader := e2e.SplitFile(f.Path, int(rr.Start), int(rr.Length()))

			w.Header().Set("Content-Length", strconv.Itoa(int(rr.Length())))
			w.Header().Set("Content-Range", rr.ContentRange(uint64(f.Size)))
			w.WriteHeader(http.StatusPartialContent)

			n, err := io.Copy(w, reader)
			if err != nil {
				t.Fatal(err)
				return
			}

			assert.Equal(t, n, rr.Length(), "copy bytes should be equal to range length")
		}))
		defer case1.Close()

		rr := xhttp.NewRequestRange(600000, 0)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", rr.String())
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, int(rr.Start), int(rr.RangeLength(int64(f.Size))))

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, rr.ContentRange(uint64(f.Size)), resp.Header.Get("Content-Range"), "response should be Content-Range")

		assert.Equal(t, "new-file", resp.Header.Get("X-Case"), "response should be X-Case new-file")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "response should be ETag")

		assert.Equal(t, newlm, resp.Header.Get("Last-Modified"), "response should be Last-Modified")

		assert.Equal(t, hashBody, hashFile, "response body should be equal to file MD5")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/lm/file1.apk")
	})
}
