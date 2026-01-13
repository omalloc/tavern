package range_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omalloc/tavern/pkg/e2e"
	"github.com/stretchr/testify/assert"
)

func init() {
	e2e.SetLocalAddr("/tmp/tavern.sock")
}

func TestRangeOffset(t *testing.T) {
	f := e2e.GenFile(t, 2<<20) // 5M

	t.Run("test range offset bytes=0-524287 MISS", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/normal/1-1", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		size := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "etag should be equal")

		assert.Equal(t, int64(524288), size, "size should be 524288")

		assert.Contains(t, resp.Header.Get("X-Cache"), "MISS", "cache should be MISS")
	})

	t.Run("test range offset bytes=0-524287 HIT", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/normal/1-1", e2e.WrongHit(t))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		size := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Contains(t, resp.Header.Get("X-Cache"), "HIT", "cache should be HIT")

		assert.Equal(t, int64(524288), size, "size should be 524288")
	})

	t.Run("test range offset bytes=524288-1048575 PART_MISS", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/normal/1-1", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=524288-1048575")
		})

		size := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Contains(t, resp.Header.Get("X-Cache"), "PART_MISS", "cache should be PART_MISS")

		assert.Equal(t, int64(524288), size, "size should be 524288")
	})

	t.Run("test range offset bytes=524288-1572863 PART_HIT", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/normal/1-1", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=524288-1572863")
		})

		hashStr := e2e.HashBody(resp)
		hashStr2 := e2e.HashFile(f.Path, 524288, 1048576)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Contains(t, resp.Header.Get("X-Cache"), "PART_HIT", "cache should be PART_HIT")

		assert.Equal(t, int64(1048576), resp.ContentLength, "size should be 1048576")

		assert.Equal(t, hashStr, hashStr2, "HASH should be equal")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/range/normal/1-1")
	})
}

func TestRangeOffsetOverflow(t *testing.T) {
	f := e2e.GenFile(t, 5<<20)

	t.Run("test range offset bytes=0-524287 MISS", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/overflow/1-1", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		size := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, int64(524288), size, "size should be 524288")
	})

	t.Run("test range offset end-overflow bytes=5242880-", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/overflow/1-1", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=5242880-")
			r.Header.Set("X-Request-ID", t.Name())
		})

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.StatusCode, "response should be code 416")
	})

	t.Run("test range offset start-overflow bytes=5242877-", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/overflow/1-1", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=5242878-")
		})

		size := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusPartialContent, resp.StatusCode, "response should be code 206")

		assert.Equal(t, int64(2), size, "size should be 2")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/range/overflow/1-1")
	})
}

func TestRangeExpiredRefresh(t *testing.T) {
	f := e2e.GenFile(t, 5<<20)

	t.Run("test range full-cache fill", func(t *testing.T) {
		// etag and cache-control should be set
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/full-cache/1-1", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "max-age=2")
			w.Header().Set("ETag", f.MD5)
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {})

		size := e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Contains(t, resp.Header.Get("X-Cache"), "MISS")

		assert.Equal(t, int64(f.Size), size, "size should be file size")
	})

	t.Run("test range full-cache HIT", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/full-cache/1-1", e2e.WrongHit(t))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, 0, 524288)

		assert.NoError(t, err, "response should not error")

		assert.Contains(t, resp.Header.Get("X-Cache"), "HIT")

		assert.Equal(t, int64(524288), resp.ContentLength, "size should be file size")

		assert.Equal(t, hashBody, hashFile, "HASH should be equal")
	})

	t.Run("test range full-cache REFRESH_HIT 200", func(t *testing.T) {
		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/full-cache/1-1", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			et := r.Header.Get("If-None-Match")
			lm := r.Header.Get("If-Modified-Since")

			assert.Equal(t, et, f.MD5)
			assert.NotNil(t, lm)

			w.Header().Set("X-Protocol-Request-ID", uuid.NewString())

			if strings.EqualFold(et, f.MD5) {
				w.Header().Set("Cache-Control", "max-age=2")
				w.Header().Set("ETag", f.MD5)
				w.WriteHeader(http.StatusNotModified)
				return
			}

			// 本应该 ETag matched, 500 就证明错误了
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer case1.Close()

		time.Sleep(2 * time.Second)

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, 0, 524288)

		assert.NoError(t, err, "response should not error")

		assert.Contains(t, resp.Header.Get("X-Cache"), "REVALIDATE_HIT")

		assert.Equal(t, int64(524288), resp.ContentLength, "size should be file size")

		assert.Equal(t, hashBody, hashFile, "HASH should be equal")
	})

	t.Run("test range full-cache changed REFRESH_MISS", func(t *testing.T) {
		// gen new file
		f = e2e.GenFile(t, 5<<20)

		// 等 2s 后, 文件内容已经改变
		time.Sleep(2 * time.Second)

		case1 := e2e.New("http://sendya.me.gslb.com/cases/range/full-cache/1-1", e2e.RespCallbackFile(f, func(w http.ResponseWriter, r *http.Request) {
			et := r.Header.Get("If-None-Match")
			lm := r.Header.Get("If-Modified-Since")

			assert.NotEqual(t, et, f.MD5)
			assert.NotNil(t, lm)

			w.Header().Set("X-Protocol-Request-ID", uuid.NewString())
			if strings.EqualFold(et, f.MD5) {
				w.Header().Set("Cache-Control", "max-age=2")
				w.Header().Set("ETag", f.MD5)
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Header.Set("Range", "bytes=0-524287")
		})

		hashBody := e2e.HashBody(resp)
		hashFile := e2e.HashFile(f.Path, 0, 524288)

		assert.NoError(t, err, "response should not error")

		assert.Contains(t, resp.Header.Get("X-Cache"), "REVALIDATE_MISS")

		assert.Equal(t, int64(524288), resp.ContentLength, "size should be file size")

		assert.Equal(t, hashBody, hashFile, "HASH should be equal")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://sendya.me.gslb.com/cases/range/full-cache/1-1")
	})
}
