package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/omalloc/proxy/selector"
)

func TestLegacyCollapseFlagCannotShareDifferentVaryRequests(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", r.Header.Get("Accept"))
		w.Header().Set("Vary", "Accept")
		io.WriteString(w, r.Header.Get("Accept"))
	}))
	defer origin.Close()
	p := New(WithInitialNodes([]selector.Node{selector.NewNode("http", origin.Listener.Addr().String(), nil)}))
	results := make(chan error, 2)
	for _, mime := range []string{"image/png", "image/jpeg"} {
		go func() {
			req, _ := http.NewRequestWithContext(t.Context(), "GET", origin.URL+"/image", nil)
			req.Header.Set("Accept", mime)
			resp, err := p.Do(req, true, time.Second)
			if err == nil {
				data, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				err = readErr
				if string(data) != mime || resp.Header.Get("Content-Type") != mime {
					err = fmt.Errorf("crossed proxy response: %q", data)
				}
			}
			results <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("legacy proxy collapse path still shared a request")
		}
	}
	unblock()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
