package proxy

import (
	"net/http"

	"github.com/omalloc/proxy/selector"
	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

// Transport is the bottom of the middleware chain. Selection, origin metrics
// and connection pools remain owned by Proxy. Unverified response sharing is off.
type Transport struct{ Proxy Proxy }

var _ http.RoundTripper = Transport{}

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	upstream := req.Clone(req.Context())
	upstream.RequestURI = ""
	if upstream.URL.Host == "" {
		upstream.URL.Host = req.Host
	}
	if upstream.URL.Scheme == "" {
		upstream.URL.Scheme = xhttp.Scheme(req)
	}
	xhttp.RemoveHopByHopHeaders(upstream.Header)
	if address := req.Header.Get(protocol.InternalUpstreamAddr); address != "" {
		upstream = upstream.WithContext(selector.NewPeerContext(req.Context(), selector.NewPeer(selector.NewNode("http", address, nil))))
	}
	return t.Proxy.Do(upstream, false, 0)
}
