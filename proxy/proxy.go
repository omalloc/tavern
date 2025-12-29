package proxy

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/omalloc/proxy/selector"
	"github.com/omalloc/proxy/selector/node/direct"
	"github.com/omalloc/proxy/selector/random"

	"github.com/omalloc/tavern/proxy/singleflight"
)

type Tuple struct {
	L *http.Response
	R []byte
}

type Proxy interface {
	Do(req *http.Request, collapsed bool, waitTimeout time.Duration) (*http.Response, error)
	DoLoopback(req *http.Request) (*http.Response, error)
	Apply(nodes []selector.Node)
}

type ReverseProxy struct {
	// Rebalancer is nodes rebalancer.
	selector.Rebalancer
	*direct.Builder

	mu           sync.RWMutex
	activateMock func(*http.Client)
	selector     selector.Selector

	clientMap map[string]*http.Client // addr -> http.Client
	dialer    *net.Dialer
	flight    *singleflight.Group
}

type Option func(*ReverseProxy)

func New(opts ...Option) *ReverseProxy {
	r := &ReverseProxy{
		mu:        sync.RWMutex{},
		Builder:   &direct.Builder{},
		clientMap: make(map[string]*http.Client, 16),
		dialer: &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		},
		selector: random.NewBuilder().Build(), // default algorithm is random
		flight:   &singleflight.Group{},
	}

	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *ReverseProxy) Do(req *http.Request, collapsed bool, waitTimeout time.Duration) (*http.Response, error) {
	current, done, err := r.selector.Select(req.Context())
	if err != nil {
		return nil, selector.ErrNoAvailable
	}

	defer done(req.Context(), selector.DoneInfo{
		Err:           err,
		BytesSent:     true,
		BytesReceived: true,
	})

	client := r.find(current.Address())
	if !collapsed {
		return r.uncompress(client.Do(req))
	}

	ret := <-r.flight.DoChan(onceKey(req), waitTimeout, func() (*http.Response, error) {
		return r.uncompress(client.Do(req))
	})

	if ret.Err != nil {
		return ret.Val, ret.Err
	}

	if ret.Shared {
		// if shared, process the response copied.
		return ret.Val, ret.Err
	}
	// return directly
	return ret.Val, ret.Err
}

func (r *ReverseProxy) DoLoopback(req *http.Request) (*http.Response, error) {
	client := r.find("127.0.0.1:8888")
	return client.Do(req)
}

func (r *ReverseProxy) find(addr string) *http.Client {
	r.mu.RLock()
	if client, ok := r.clientMap[addr]; ok {
		r.mu.RUnlock()
		return client
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()

	network := "tcp"
	if strings.HasSuffix(addr, ".sock") || strings.HasPrefix(addr, "unix://") {
		network = "unix"
		addr = strings.TrimPrefix(addr, "unix://")
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxConnsPerHost:       100,
			MaxIdleConns:          1000,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       10 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DisableCompression:    true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return r.dialer.DialContext(ctx, network, addr)
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	if r.activateMock != nil {
		r.activateMock(client)
	}

	r.clientMap[addr] = client

	return client
}

func (r *ReverseProxy) uncompress(resp *http.Response, err error) (*http.Response, error) {
	if err != nil {
		return resp, err
	}

	encoding := resp.Header.Get("Content-Encoding")
	switch encoding {
	case "gzip":
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return resp, err
		}
		resp.ContentLength = -1
		resp.Body = reader
	case "br":
		reader := brotli.NewReader(resp.Body)
		resp.ContentLength = -1
		resp.Body = &struct {
			io.Closer
			io.Reader
		}{
			Closer: resp.Body,
			Reader: reader,
		}
	}
	return resp, err
}

func onceKey(req *http.Request) string {
	sb := strings.Builder{}
	sb.WriteString(req.Method)
	sb.WriteString(req.URL.String())
	sb.WriteString(req.Header.Get("Range"))
	return sb.String()
}

// Apply is apply all nodes when any changes happen
func (r *ReverseProxy) Apply(nodes []selector.Node) {
	r.selector.Apply(nodes)
}

// WithInitialNodes is set initial nodes
func WithInitialNodes(nodes []selector.Node) Option {
	return func(r *ReverseProxy) {
		r.selector.Apply(nodes)
	}
}

// WithSelector is set new-selector
func WithSelector(s selector.Selector) Option {
	return func(r *ReverseProxy) {
		r.selector = s
	}
}

// WithDialer is set custom net.Dialer
func WithDialer(d *net.Dialer) Option {
	return func(r *ReverseProxy) {
		r.dialer = d
	}
}

// WithActivateMock is activate httpmock
func WithActivateMock(fn func(client *http.Client)) Option {
	return func(r *ReverseProxy) {
		r.activateMock = fn
	}
}
