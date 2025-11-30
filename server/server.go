package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudflare/tableflip"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	pluginv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/contrib/transport"
	"github.com/omalloc/tavern/server/middleware"
)

var localMatcher = map[string]struct{}{
	"localhost": {},
	"127.1":     {},
	"127.0.0.1": {},
}

type MergeableConfig struct {
}

type HTTPServer struct {
	*http.Server

	plugins []pluginv1.Plugin

	flip     *tableflip.Upgrader
	config   *conf.Bootstrap
	listener net.Listener
	cleanups []func()
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
		flip:     flip,
		config:   config,
		cleanups: make([]func(), 0),
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

	// internal handlers
	// TODO: profiles handler

	mux.Handle("/favicon.ico", http.NotFoundHandler())
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

	return mux
}

func (s *HTTPServer) buildEndpoint() (http.HandlerFunc, error) {
	tripper, err := s.buildMiddlewareChain(nil)
	if err != nil {
		return nil, err
	}

	next := s.buildHandler(tripper)

	// Let plugins handle the request.
	for _, plug := range s.plugins {
		plug.HandleFunc(next)
	}

	// add access-log handler

	return next, nil
}

// Cache 主流程入口
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
