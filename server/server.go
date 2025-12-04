package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cloudflare/tableflip"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/contrib/transport"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/server/middleware"
	_ "github.com/omalloc/tavern/server/middleware/caching"
	_ "github.com/omalloc/tavern/server/middleware/recovery"
	_ "github.com/omalloc/tavern/server/middleware/rewrite"
	"github.com/omalloc/tavern/server/mod"
)

var localMatcher = map[string]struct{}{
	"localhost": {},
	"127.1":     {},
	"127.0.0.1": {},
}
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}

type HTTPServer struct {
	*http.Server

	plugins []pluginv1.Plugin

	flip         *tableflip.Upgrader
	config       *conf.Bootstrap
	serverConfig *conf.Server
	listener     net.Listener
	cleanups     []func()
}

func NewServer(flip *tableflip.Upgrader, config *conf.Bootstrap, plugins []pluginv1.Plugin) transport.Server {
	servConfig := config.Server

	s := &HTTPServer{
		Server: &http.Server{
			Addr:              servConfig.Addr,
			ReadTimeout:       servConfig.ReadTimeout,
			WriteTimeout:      servConfig.WriteTimeout,
			IdleTimeout:       servConfig.IdleTimeout,
			ReadHeaderTimeout: servConfig.ReadHeaderTimeout,
			MaxHeaderBytes:    servConfig.MaxHeaderBytes,
		},
		flip:         flip,
		config:       config,
		serverConfig: config.Server,
		cleanups:     make([]func(), 0),
	}

	// 初始化内部路由
	// - 探测接口
	// - 监控接口
	// - 查询接口
	// - 用于注册插件的路由
	mux := s.newServeMux()

	// 初始化业务服务的路由监听
	next, err := s.buildEndpoint()
	if err != nil {
		panic(err)
	}

	fmtAddr := func(addr string) string {
		if i := strings.IndexByte(addr, ':'); i >= 0 {
			return addr[:i]
		}
		return addr
	}

	s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := fmtAddr(r.Host)
		if _, ok := localMatcher[host]; ok {
			// 内部接口处理流程
			mux.ServeHTTP(w, r)
			return
		}

		// 主业务流程
		next(w, r)
	})

	return s
}

func (s *HTTPServer) Start(ctx context.Context) error {

	s.BaseContext = func(ln net.Listener) context.Context {
		return ctx
	}

	if err := s.listen(); err != nil {
		return err
	}

	log.Infof("HTTP Cache server listening on %s", s.config.Server.Addr)

	if err := s.Serve(s.listener); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

func (s *HTTPServer) Stop(ctx context.Context) error {
	var errs []error

	if err := s.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	// Call all middleware cleanup.
	for _, cleanup := range s.cleanups {
		cleanup()
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (s *HTTPServer) newServeMux() *http.ServeMux {
	mux := http.NewServeMux()

	// profiles handler
	mod.HandlePProf(s.serverConfig.PProf, mux)
	// internal handlers
	mux.Handle("/favicon.ico", http.NotFoundHandler())
	// metrics
	mux.Handle("/metrics", promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	// 启动探针
	mux.Handle("/healthz/startup-probe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := []byte("ok")

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	// 存活探针
	mux.Handle("/healthz/liveness-probe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// 就绪探针
	mux.Handle("/healthz/readiness-probe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 初始化插件的路由监听(如果插件需要)
	for _, plug := range s.plugins {
		plug.AddRouter(mux)
	}

	xhttp.PrintRoutes(mux)

	return mux
}

// buildHandler ... Cache 主流程入口
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

			// 如果上游没返回业务错误，则直接响应 500
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Length", bodyLen)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(bodyBytes)

			_metricRequestsTotal.WithLabelValues(req.Proto, strconv.Itoa(http.StatusInternalServerError)).Inc()
			return
		}

		// response now
		headers := w.Header()
		xhttp.CopyHeader(headers, resp.Header)
		// see https://pkg.go.dev/net/http#example-ResponseWriter-Trailers
		xhttp.CopyTrailer(headers, resp.Trailer)

		w.WriteHeader(resp.StatusCode)

		doCopyBody := func() {
			if resp.Body == nil {
				_metricRequestsTotal.WithLabelValues(req.Proto, strconv.Itoa(resp.StatusCode)).Inc()
				return
			}

			// HEAD request skip copy body
			if req.Method == http.MethodHead {
				return
			}

			buf := bufPool.Get().(*[]byte)
			defer func() {
				_ = resp.Body.Close()
				bufPool.Put(buf)

				_metricRequestsTotal.WithLabelValues(req.Proto, strconv.Itoa(resp.StatusCode)).Inc()
			}()

			want := resp.Header.Get("Content-Length")

			sent, err := io.CopyBuffer(w, resp.Body, *buf)
			if err != nil {
				// abort ? continue ?
				clog.Errorf("failed to copy upstream response body to client: [%s] %s %s sent=%d want=%s err=%s", req.Proto, req.Method, req.URL.Path, sent, want, err)
				_metricRequestUnexpectedClosed.WithLabelValues(req.Proto, req.Method).Inc()
				return
			}

			if slices.Contains(resp.TransferEncoding, "chunked") || want == "" {
				clog.Debugf("copied %d bytes chunked body from upstream to client", sent)
				return
			}

			want1, _ := strconv.ParseInt(want, 10, 64)
			if sent != want1 {
				clog.Warnf("copied %d bytes from upstream to client, conflict Content-Length %s bytes", sent, want)
				return
			}

			clog.Debugf("copied %d bytes from upstream to client, Content-Length %s bytes", sent, want)
		}

		doCopyBody()
	}
}

func (s *HTTPServer) buildEndpoint() (http.HandlerFunc, error) {
	tripper, err := s.buildMiddlewareChain(nil)
	if err != nil {
		return nil, err
	}

	// build the final handler
	next := s.buildHandler(tripper)

	// Let plugins handle the request.
	for _, plug := range s.plugins {
		plug.HandleFunc(next)
	}

	// add access-log handler
	return mod.HandleAccessLog(s.serverConfig.AccessLog, next), nil
}

func (s *HTTPServer) buildMiddlewareChain(tripper http.RoundTripper) (http.RoundTripper, error) {
	conf := s.config.Server.Middleware
	for i := len(conf) - 1; i >= 0; i-- {
		if conf[i].Name == "" {
			panic("middlewares name is empty, config file array index " + strconv.Itoa(i))
		}

		middlewareConf := conf[i]
		next, cleanup, err := middleware.Create(middlewareConf)
		if err != nil {
			log.Warnf("failed to create middleware %s: %v", middlewareConf.Name, err)
			continue
		}

		s.cleanups = append(s.cleanups, cleanup)

		tripper = next(tripper)
	}
	return tripper, nil
}
