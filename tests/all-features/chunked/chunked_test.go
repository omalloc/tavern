package chunked_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChunkedInterrupted verifies client behavior when the server aborts
// a chunked response while the body is still being read.
func TestChunkedInterrupted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}

		_, _ = w.Write([]byte("partial-body"))
		flusher.Flush()

		conn, _, err := hijacker.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, readErr := io.ReadAll(resp.Body)
	t.Logf("body %s readErr %v", body, readErr)
	require.Error(t, readErr)
	require.True(t, errors.Is(readErr, io.ErrUnexpectedEOF))
	require.Equal(t, "partial-body", string(body))
}
