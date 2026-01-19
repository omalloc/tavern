package errcode_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/internal/constants"
	"github.com/omalloc/tavern/pkg/e2e"
)

func init() {
	e2e.SetLocalAddr("/tmp/tavern.sock")
}

func TestErrCodeNoCache(t *testing.T) {

	t.Run("test errcode no cache", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/errcode/no-cache", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(
				[]byte(http.StatusText(http.StatusBadGateway)),
			)
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
		})

		assert.NoError(t, err, "response should not error")

		_ = e2e.DiscardBody(resp, 512)

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode, "response should be code 502")
	})

	t.Run("test errcode no cache NO HIT", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/errcode/no-cache", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(
				[]byte(http.StatusText(http.StatusBadGateway)),
			)
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
		})

		assert.NoError(t, err, "response should not error")

		_ = e2e.DiscardBody(resp, 512)

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode, "response should be code 502")

		xCache := resp.Header.Get("X-Cache")

		assert.NotContains(t, xCache, "HIT", "cache should be MISS")
	})

}

func TestErrCodeCache(t *testing.T) {

	t.Run("test errcode cache now", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/errcode/cache", e2e.RespCallback(func(w http.ResponseWriter, r *http.Request) {
			payload := []byte(http.StatusText(http.StatusBadGateway))

			w.Header().Set(constants.CacheTime, "30")           // 强制缓存30秒
			w.Header().Set(constants.InternalCacheErrCode, "1") // 开启缓存错误状态码
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(payload)
		}))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
		})

		assert.NoError(t, err, "response should not error")

		_ = e2e.DiscardBody(resp, 512)

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode, "response should be code 502")
	})

	t.Run("test errcode cache hit", func(t *testing.T) {
		case1 := e2e.New("http://example.com.gslb.com/errcode/cache", e2e.WrongHit(t))
		defer case1.Close()

		resp, err := case1.Do(func(r *http.Request) {
		})

		assert.NoError(t, err, "response should not error")

		_ = e2e.DiscardBody(resp, 512)

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode, "response should be code 502")

		xCache := resp.Header.Get("X-Cache")

		assert.Contains(t, xCache, "HIT", "cache should be MISS")
	})

	t.Run("PURGE", func(t *testing.T) {
		e2e.Purge(t, "http://example.com.gslb.com/errcode/cache")
	})

}
