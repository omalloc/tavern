package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
)

func TestLiveSIGUSR2DrainsFillBeforeNewWriter(t *testing.T) {
	if configPath := os.Getenv("TAVERN_UPGRADE_CONFIG"); configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		config := &conf.Bootstrap{}
		if err = json.Unmarshal(data, config); err != nil {
			t.Fatal(err)
		}
		logger := log.NewFilter(log.NewStdLogger(os.Stderr), log.FilterLevel(log.LevelWarn))
		log.SetLogger(logger)
		app, err := newApp(config, logger)
		if err != nil {
			t.Fatal(err)
		}
		if err = app.Run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	base, err := os.MkdirTemp("/tmp", "tavern-upgrade-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	entered := make(chan struct{})
	var once sync.Once
	var originCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Vary", "Accept")
		if r.URL.Path == "/slow" {
			w.Header().Set("Content-Length", "65536")
			io.WriteString(w, strings.Repeat("x", 8192))
			w.(http.Flusher).Flush()
			once.Do(func() { close(entered) })
			<-r.Context().Done()
			return
		}
		originCalls.Add(1)
		body := r.Header.Get("Accept")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		fmt.Fprint(w, body)
	}))
	defer origin.Close()
	config := &conf.Bootstrap{PidFile: filepath.Join(base, "pid"), Logger: &conf.Logger{}, Upstream: &conf.Upstream{Address: []string{origin.URL}}, Server: &conf.Server{Addr: filepath.Join(base, "http.sock"), ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, AccessLog: &conf.ServerAccessLog{}, PProf: &conf.ServerPProf{}, Middleware: []*middlewarev1.Middleware{{Name: "caching", Required: true, Options: map[string]any{"include_query_in_cache_key": true, "slice_size": 64, "representation": map[string]any{"enabled": true}}}}}, Storage: &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring", SliceSize: 64, DirAware: &conf.DirAware{}, Representation: &conf.RepresentationStorage{StateDirectory: filepath.Join(base, "state")}, Buckets: []*conf.Bucket{{Path: filepath.Join(base, "data"), MaxObjectLimit: 100}}}}
	data, _ := json.Marshal(config)
	configPath := filepath.Join(base, "config.json")
	if err = os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(base, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestLiveSIGUSR2DrainsFillBeforeNewWriter$", "-test.count=1", "-test.timeout=40s")
	command.Env = append(os.Environ(), "TAVERN_UPGRADE_CONFIG="+configPath)
	command.Stdout, command.Stderr = output, output
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()
	pid := func() int {
		raw, _ := os.ReadFile(config.PidFile)
		value, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		return value
	}
	defer func() {
		if current := pid(); current > 0 {
			syscall.Kill(current, syscall.SIGTERM)
		}
		command.Process.Kill()
		select {
		case <-processDone:
		case <-time.After(time.Second):
		}
	}()
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", config.Server.Addr)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	fail := func(message string) {
		t.Helper()
		logs, _ := os.ReadFile(filepath.Join(base, "process.log"))
		t.Fatalf("%s\n%s", message, logs)
	}
	deadline := time.Now().Add(10 * time.Second)
	for pid() == 0 {
		if time.Now().After(deadline) {
			fail("initial writer did not become ready")
		}
		select {
		case err := <-processDone:
			fail(fmt.Sprintf("initial process exited: %v", err))
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	oldPID := pid()
	slowDone := make(chan struct{})
	go func() {
		resp, err := client.Get("http://cache.example/slow")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		close(slowDone)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		fail("active fill did not reach origin")
	}
	if err = syscall.Kill(oldPID, syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for pid() == 0 || pid() == oldPID {
		if time.Now().After(deadline) {
			fail("replacement writer did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-slowDone:
	case <-time.After(5 * time.Second):
		fail("old fill remained alive after writer handoff")
	}
	for _, mime := range []string{"image/jpeg", "image/webp", "image/jpeg", "image/webp"} {
		req, _ := http.NewRequest("GET", "http://cache.example/image", nil)
		req.Header.Set("Accept", mime)
		resp, err := client.Do(req)
		if err != nil {
			fail(fmt.Sprint(err))
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != mime {
			fail(fmt.Sprintf("new writer returned %q: %v", body, err))
		}
	}
	if originCalls.Load() != 2 {
		fail(fmt.Sprintf("new writer did not cache variants: %d origin calls", originCalls.Load()))
	}
	select {
	case err := <-processDone:
		if err != nil {
			fail(fmt.Sprintf("old process exited with %v", err))
		}
		processDone <- nil
	case <-time.After(5 * time.Second):
		fail("old writer did not exit")
	}
}
