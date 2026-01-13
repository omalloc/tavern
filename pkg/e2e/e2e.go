package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/omalloc/tavern/internal/constants"
)

var (
	mu        sync.RWMutex
	localAddr = "/tmp/tavern.sock" // default local address
	dump      = atomic.Bool{}
	dumpReq   = atomic.Bool{}

	manual = atomic.Bool{}
)

type E2E struct {
	caseUrl    string
	srcHandler http.Handler
	ts         *httptest.Server
	cs         *http.Client
	req        *http.Request
	resp       *http.Response
	err        error
}

func New(caseUrl string, srcHandler http.HandlerFunc) *E2E {
	e := &E2E{
		caseUrl:    caseUrl,
		srcHandler: srcHandler,
	}

	u, err := url.Parse(caseUrl)
	if err != nil {
		return e
	}

	if e.srcHandler == nil {
		e.srcHandler = http.NotFoundHandler()
	}

	e.ts = httptest.NewServer(e.srcHandler)

	dialer := &net.Dialer{}

	// replace the default transport with a custom one that uses the local address
	e.ts.Client().Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			mu.RLock()
			addr := localAddr
			mu.RUnlock()

			if strings.HasSuffix(addr, ".sock") {
				return dialer.DialContext(ctx, "unix", addr)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	e.cs = e.ts.Client()

	req, err := http.NewRequest(http.MethodGet, e.caseUrl, nil)
	if err != nil {
		panic(err)
	}

	req.Header.Set("X-Test-Case", u.Path)
	req.Header.Set("X-Store-Url", u.String())

	e.req = req
	return e
}

func (e *E2E) Do(rewrite func(r *http.Request)) (*http.Response, error) {
	rewrite(e.req)

	method := e.req.Method

	e.req.Header.Set(constants.InternalUpstreamAddr, e.ts.Listener.Addr().String())

	if dumpReq.Load() && method != "PURGE" {
		DumpReq(e.req)
	}

	if manual.Load() {
		fmt.Printf("manual mode wait 20s, src addr %q\n", e.ts.Listener.Addr().String())
		time.Sleep(time.Second * 20)
	}

	resp, err := e.cs.Do(e.req)
	e.resp = resp
	e.err = err

	// PURGE 不打印响应
	if dump.Load() && method != "PURGE" {
		DumpResp(resp)
	}

	// wait for a while to let the connection close properly
	time.Sleep(time.Millisecond * 200)

	return resp, err
}

func (e *E2E) Close() {
	e.ts.Close()
	if e.resp != nil && e.resp.Body != nil {
		_ = e.resp.Body.Close()
	}
}

func SetLocalAddr(addr string) {
	mu.Lock()
	defer mu.Unlock()

	localAddr = addr
}

func SetDump(b bool) {
	dump.Store(b)
}

func SetDumpReq(b bool) {
	dumpReq.Store(b)
}

func SetManual(b bool) {
	manual.Store(b)
}

func DumpReq(req *http.Request) {
	if req == nil {
		fmt.Println("request is nil")
		return
	}

	buf, err := httputil.DumpRequest(req, false)
	if err != nil {
		return
	}
	fmt.Println()
	fmt.Println(string(buf))
}

func DumpResp(resp *http.Response) {
	if resp == nil {
		fmt.Println("response is nil")
		return
	}

	buf, err := httputil.DumpResponse(resp, false)
	if err != nil {
		return
	}
	fmt.Println()
	fmt.Println(string(buf))
}

func Purge(t *testing.T, url string) {
	resp, err := New(url, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}).Do(func(r *http.Request) {
		r.Method = "PURGE"
	})

	assert.NoError(t, err, "purge should not error")

	assert.Contains(t, []int{http.StatusOK, http.StatusNotFound}, resp.StatusCode, "PURGE should be 404 or 200")

	if dump.Load() {
		t.Logf("Purge %s success", url)
	}
}
