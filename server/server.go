package server

import (
	"context"
	"net"
	"net/http"

	"github.com/cloudflare/tableflip"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/contrib/transport"
)

type HTTPServer struct {
	*http.Server

	plugins []pluginv1.Plugin

	flip     *tableflip.Upgrader
	listener net.Listener
	cleanups []func() error
}

func NewServer(flip *tableflip.Upgrader, plugins []pluginv1.Plugin) transport.Server {
	return &HTTPServer{
		Server:   &http.Server{},
		flip:     flip,
		cleanups: make([]func() error, 0),
	}
}

func (s *HTTPServer) Start(ctx context.Context) error {
	return nil
}

func (s *HTTPServer) Stop(ctx context.Context) error {
	return s.Shutdown(ctx)
}

func (s *HTTPServer) buildHandler(tripper http.RoundTripper) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var clog = log.Context(req.Context())
		var resp *http.Response
		var err error

		// finally close response body
		defer func() {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}()

		resp, err = tripper.RoundTrip(req)
		if err != nil {
			clog.Errorf("request %s %s failed: %s", req.Method, req.URL.Path, err)
		}

		doCopyBody := func() {
			if resp.Body == nil {
				return
			}

			// HEAD request skip copy body
			if req.Method == http.MethodHead {
				return
			}

		}

		doCopyBody()
	}
}

func (s *HTTPServer) buildEndpoint() (http.HandlerFunc, error) {
	return nil, nil
}

func (s *HTTPServer) newServeMux() *http.ServeMux {
	mux := http.NewServeMux()

	return mux
}
