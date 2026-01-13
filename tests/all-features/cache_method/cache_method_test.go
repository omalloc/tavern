package cache_method_test

import (
	"net/http"
	"testing"

	"github.com/omalloc/tavern/pkg/e2e"
	"github.com/stretchr/testify/assert"
)

func TestCacheMethodAllowGETorHEAD(t *testing.T) {
	f := e2e.GenFile(t, 1<<20)

	t.Run("test cache method allow GET", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/cache-method/get", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Method = http.MethodGet
		})

		assert.NoError(t, err, "response should not error")

		size := e2e.DiscardBody(resp)

		assert.Equal(t, f.Size, size, "size should be file size")

		assert.Equal(t, http.StatusOK, resp.StatusCode, "response should be code 200")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "etag should be equal")

		assert.Contains(t, resp.Header.Get("X-Cache"), "MISS", "cache should be MISS")
	})

	t.Run("test cache method allow GET be hit", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/cache-method/get", e2e.WrongHit(t))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Method = http.MethodGet
		})

		assert.NoError(t, err, "response should not error")

		size := e2e.DiscardBody(resp)

		assert.Equal(t, f.Size, size, "size should be file size")

		assert.Equal(t, http.StatusOK, resp.StatusCode, "response should be code 200")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "etag should be equal")

		assert.Contains(t, resp.Header.Get("X-Cache"), "HIT", "cache should be HIT")
	})

	t.Run("test cache method allow HEAD", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/cache-method/get", e2e.RespSimpleFile(f))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
			r.Method = http.MethodGet
		})

		_ = e2e.DiscardBody(resp)

		assert.NoError(t, err, "response should not error")

		assert.Equal(t, http.StatusOK, resp.StatusCode, "response should be code 200")

		assert.Equal(t, f.MD5, resp.Header.Get("ETag"), "etag should be equal")

		assert.Contains(t, resp.Header.Get("X-Cache"), "HIT", "cache should be HIT")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://example.com.gslb.com/cache-method/get")
	})
}
