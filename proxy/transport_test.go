package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/omalloc/tavern/internal/protocol"
)

type transportProbe struct {
	Proxy
	request   *http.Request
	collapsed bool
	timeout   time.Duration
}

func (p *transportProbe) Do(req *http.Request, collapsed bool, timeout time.Duration) (*http.Response, error) {
	p.request, p.collapsed, p.timeout = req, collapsed, timeout
	return &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody}, nil
}
func TestTransportPreservesCallerContextAndDisablesSharing(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), key{}, "value"), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com/a", nil)
	req.RequestURI = "/a"
	req.Header.Set(protocol.InternalUpstreamAddr, "127.0.0.1:8888")
	p := &transportProbe{}
	if _, err := (Transport{Proxy: p}).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if p.request == req || p.request.RequestURI != "" || req.RequestURI != "/a" || p.collapsed || p.timeout != 0 {
		t.Fatal("unsafe transport request sharing or mutation")
	}
	if p.request.Context().Value(key{}) != "value" {
		t.Fatal("peer context dropped caller values")
	}
	if a, ok := p.request.Context().Deadline(); !ok {
		t.Fatal("deadline lost")
	} else if b, _ := ctx.Deadline(); !a.Equal(b) {
		t.Fatal("deadline changed")
	}
	cancel()
	if p.request.Context().Err() != context.Canceled {
		t.Fatal("cancel lost")
	}
}
