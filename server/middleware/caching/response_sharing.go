package caching

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omalloc/proxy/selector"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

type responseSharingOption struct {
	Enabled      bool     `json:"enabled" yaml:"enabled"`
	MaxBodyBytes int64    `json:"max_body_bytes" yaml:"max_body_bytes"`
	MaxFlights   int      `json:"max_flights" yaml:"max_flights"`
	MaxWaiters   int      `json:"max_waiters" yaml:"max_waiters"`
	WaitTimeout  Duration `json:"wait_timeout" yaml:"wait_timeout"`
}

func (o *cachingOption) bindResponseSharing() error {
	if o.ResponseSharing == nil || !o.ResponseSharing.Enabled {
		return nil
	}
	if o.representation == nil || !o.representation.writes {
		return fmt.Errorf("response_sharing requires caching.representation.enabled")
	}
	config := *o.ResponseSharing
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = 1 << 20
	}
	if config.MaxFlights == 0 {
		config.MaxFlights = 32
	}
	if config.MaxWaiters == 0 {
		config.MaxWaiters = 32
	}
	if config.WaitTimeout == "" {
		config.WaitTimeout = "1s"
	}
	if config.MaxBodyBytes < 1 || config.MaxBodyBytes > 8<<20 || config.MaxFlights < 1 || config.MaxFlights > 128 || config.MaxWaiters < 1 || config.MaxWaiters > 128 || config.WaitTimeout.AsDuration() <= 0 || config.WaitTimeout.AsDuration() > 30*time.Second {
		return fmt.Errorf("invalid response_sharing budgets")
	}
	o.sharing = &responseSharing{config: config, calls: make(map[[32]byte]*responseCall)}
	return nil
}

// A response fetched before PURGE/soft PURGE or a location change cannot become
// the origin response of a newly captured fill. This trusted identity never
// comes from a client header and includes the cold-resource prefix barriers.
type responseScope struct {
	Resource, Incarnation, BarrierIncarnation, Location string
	Purge, Revalidate, Hard, Soft, Routing              uint64
}
type responseScopeKey struct{}
type responseAlreadyAgedKey struct{}

func scopeResponseRequest(req *http.Request, root *object.ResourceRecord) *http.Request {
	scope := responseScope{root.ResourceID, root.Incarnation, root.BarrierIncarnation, root.ActiveLocation,
		root.PurgeEpoch, root.RevalidateEpoch, root.PrefixHardEpoch, root.PrefixSoftEpoch, root.RoutingEpoch}
	return req.WithContext(context.WithValue(req.Context(), responseScopeKey{}, scope))
}

func sharingRequestSnapshot(req *http.Request) (varycontrol.Snapshot, bool) {
	if req == nil || req.URL == nil || req.Method != http.MethodGet || req.URL.User != nil || req.URL.Fragment != "" ||
		req.Close || req.ContentLength != 0 || (req.Body != nil && req.Body != http.NoBody) || len(req.TransferEncoding) != 0 || len(req.Trailer) != 0 || requestBypassesCache(req) || hasClientConditions(req) {
		return varycontrol.Snapshot{}, false
	}
	if len(req.URL.String()) > varycontrol.MaxSnapshotBytes || (req.TLS != nil && len(req.TLS.PeerCertificates) != 0) {
		return varycontrol.Snapshot{}, false
	}
	if _, ok := selector.FromPeerContext(req.Context()); ok {
		return varycontrol.Snapshot{}, false
	}
	names := []string{"Host"}
	for name, values := range req.Header {
		if name != http.CanonicalHeaderKey(name) {
			return varycontrol.Snapshot{}, false
		}
		switch name {
		case "Range", "Cookie", "Authorization", "Proxy-Authorization", "Connection", "Upgrade", "Expect", "Trailer", protocol.InternalUpstreamAddr:
			return varycontrol.Snapshot{}, false
		}
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return varycontrol.Snapshot{}, false
			}
		}
		names = append(names, name)
	}
	snapshot, err := varycontrol.Capture(names, representationSelectionHeader(req))
	return snapshot, err == nil
}

// CanReuseResponse deliberately supports exact whole GETs only. It checks the
// actual origin Vary, not an old cache index. A digest only locates a candidate;
// complete request and trusted resource identities are compared again here.
func CanReuseResponse(leader, follower *http.Request, resp *http.Response) bool {
	l, lok := sharingRequestSnapshot(leader)
	f, fok := sharingRequestSnapshot(follower)
	if !lok || !fok || resp == nil || resp.StatusCode != http.StatusOK || resp.Body == nil || resp.ContentLength < 0 ||
		len(resp.TransferEncoding) != 0 || len(resp.Trailer) != 0 || leader.URL.String() != follower.URL.String() || !reflect.DeepEqual(l, f) {
		return false
	}
	left, lok := leader.Context().Value(responseScopeKey{}).(responseScope)
	right, fok := follower.Context().Value(responseScopeKey{}).(responseScope)
	if !lok || !fok || left != right {
		return false
	}
	for name := range resp.Header {
		if name != http.CanonicalHeaderKey(name) {
			return false
		}
		switch name {
		case "Set-Cookie", "Content-Range", "Connection", "Upgrade", "Trailer", "Transfer-Encoding", "Proxy-Authenticate", "Www-Authenticate":
			return false
		}
	}
	length, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil || length != resp.ContentLength || len(resp.Header.Values("Content-Length")) != 1 {
		return false
	}
	fields, wildcard, err := varycontrol.Parse(resp.Header.Values("Vary")...)
	if err != nil || wildcard {
		return false
	}
	selected, err := varycontrol.Capture(fields, representationSelectionHeader(leader))
	if err != nil || !varycontrol.Match(selected, representationSelectionHeader(follower)) {
		return false
	}
	now := time.Now()
	expires, err := representationFreshness(resp.Header.Clone(), now, now)
	return err == nil && expires > now.Unix()
}

type responseCall struct {
	done     chan struct{}
	request  *http.Request
	response *http.Response // private immutable snapshot, never returned directly
	data     []byte
	received time.Time
	users    int // waiters and returned bodies; protected by group mutex
}

type responseSharing struct {
	config responseSharingOption
	mu     sync.Mutex
	calls  map[[32]byte]*responseCall
	active int // includes completed buffers until every reader has closed
}

func (g *responseSharing) release(call *responseCall) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releaseLocked(call)
}

func (g *responseSharing) releaseLocked(call *responseCall) {
	call.users--
	if call.users == 0 {
		g.active--
		pkgmetrics.ResponseSharingActive(-1)
		call.data, call.response = nil, nil
		call.request = nil
	}
}

func sharingKey(req *http.Request) ([32]byte, bool) {
	snapshot, ok := sharingRequestSnapshot(req)
	scope, scoped := req.Context().Value(responseScopeKey{}).(responseScope)
	if !ok || !scoped {
		return [32]byte{}, false
	}
	encoded, err := json.Marshal(struct {
		Scope   responseScope
		URL     string
		Headers varycontrol.Snapshot
	}{scope, req.URL.String(), snapshot})
	return sha256.Sum256(encoded), err == nil
}

// RoundTrip buffers only a verified, bounded complete response. No follower sees
// headers or owns a body before the reuse proof and EOF. Large/streamed/private
// responses retain the leader's ordinary stream; followers independently fetch.
// There is no detached producer: the leader's accepted cache task owns all I/O.
func (g *responseSharing) RoundTrip(origin http.RoundTripper, req *http.Request) (*http.Response, error) {
	snapshot := req.Clone(req.Context())
	key, ok := sharingKey(snapshot)
	if !ok {
		pkgmetrics.RecordResponseSharing("bypass")
		return origin.RoundTrip(req)
	}
	g.mu.Lock()
	if call := g.calls[key]; call != nil {
		if call.users >= g.config.MaxWaiters+1 {
			g.mu.Unlock()
			pkgmetrics.RecordResponseSharing("capacity")
			return origin.RoundTrip(req)
		}
		call.users++
		g.mu.Unlock()
		pkgmetrics.ResponseSharingWaiters(1)
		stopWaiting := sync.OnceFunc(func() { pkgmetrics.ResponseSharingWaiters(-1) })
		defer stopWaiting()
		timer := time.NewTimer(g.config.WaitTimeout.AsDuration())
		defer timer.Stop()
		select {
		case <-req.Context().Done():
			stopWaiting()
			pkgmetrics.RecordResponseSharing("cancelled")
			g.release(call)
			return nil, req.Context().Err()
		case <-timer.C:
			stopWaiting()
			pkgmetrics.RecordResponseSharing("timeout")
			g.release(call)
			return origin.RoundTrip(req)
		case <-call.done:
			stopWaiting()
			if req.Context().Err() != nil {
				pkgmetrics.RecordResponseSharing("cancelled")
				g.release(call)
				return nil, req.Context().Err()
			}
			if CanReuseResponse(call.request, snapshot, call.response) {
				pkgmetrics.RecordResponseSharing("shared")
				return g.respond(call, snapshot), nil
			}
			g.release(call)
			pkgmetrics.RecordResponseSharing("rejected")
			return origin.RoundTrip(req)
		}
	}
	if g.active >= g.config.MaxFlights {
		g.mu.Unlock()
		pkgmetrics.RecordResponseSharing("capacity")
		return origin.RoundTrip(req)
	}
	call := &responseCall{done: make(chan struct{}), request: snapshot, users: 1}
	g.calls[key] = call
	g.active++
	pkgmetrics.ResponseSharingActive(1)
	g.mu.Unlock()
	pkgmetrics.RecordResponseSharing("leader")
	owned := true
	defer func() {
		// On panic the ordinary recovery middleware still runs. Waiters wake and
		// retry independently rather than waiting on a permanently occupied slot.
		g.mu.Lock()
		delete(g.calls, key)
		close(call.done)
		if owned {
			g.releaseLocked(call)
		}
		g.mu.Unlock()
	}()
	started := time.Now()
	resp, err := origin.RoundTrip(req)
	if resp == nil && err == nil {
		return nil, errors.New("origin returned no response")
	}
	if err != nil || !CanReuseResponse(snapshot, snapshot, resp) || resp.ContentLength > g.config.MaxBodyBytes {
		return resp, err
	}
	call.received = time.Now()
	source := &sharingOriginBody{ReadCloser: resp.Body}
	closed := make(chan struct{})
	stop := context.AfterFunc(req.Context(), func() { defer close(closed); source.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	transferred := false
	defer func() {
		if !transferred {
			source.Close()
		}
	}()
	data, readErr := io.ReadAll(io.LimitReader(source, resp.ContentLength+1))
	if readErr != nil || int64(len(data)) != resp.ContentLength {
		// Preserve every consumed byte and the terminal read error for the leader.
		// It can still return a streaming response, but no follower reuses it.
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
			if int64(len(data)) > resp.ContentLength {
				readErr = errors.New("origin body exceeds declared content length")
			}
		}
		resp.Body = &sharingReplayBody{prefix: bytes.NewReader(data), source: source, terminal: readErr, release: func() { g.release(call) }}
		transferred = true
		owned = false
		return resp, nil
	}
	if err = source.Close(); err != nil {
		return nil, err
	}
	call.data, call.response = data, ownedResponse(resp, snapshot)
	_, _ = representationFreshness(call.response.Header, started, call.received)
	call.response.Body = http.NoBody // immutable proof of the completed buffer
	owned = false
	return g.respond(call, snapshot), nil
}

func ownedResponse(resp *http.Response, req *http.Request) *http.Response {
	copy := *resp
	copy.Header, copy.Trailer = resp.Header.Clone(), resp.Trailer.Clone()
	copy.TransferEncoding = slices.Clone(resp.TransferEncoding)
	copy.Request, copy.Body = req.Clone(req.Context()), nil
	if resp.TLS != nil {
		state := *resp.TLS
		copy.TLS = &state
	}
	return &copy
}

func (g *responseSharing) respond(call *responseCall, req *http.Request) *http.Response {
	resp := ownedResponse(call.response, req)
	resp.Request = resp.Request.WithContext(context.WithValue(resp.Request.Context(), responseAlreadyAgedKey{}, true))
	age, _ := strconv.ParseInt(resp.Header.Get("Age"), 10, 64)
	resp.Header.Set("Age", strconv.FormatInt(saturatedAge(max(0, age), max(0, int64(time.Since(call.received)/time.Second))), 10))
	resp.Body = &sharingBufferBody{reader: bytes.NewReader(call.data), release: func() { g.release(call) }}
	return resp
}

type sharingBufferBody struct {
	mu      sync.Mutex
	reader  *bytes.Reader
	release func()
}

func (b *sharingBufferBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reader == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := b.reader.Read(p)
	if n > 0 && b.reader.Len() == 0 {
		// This buffer has already reached the origin's EOF. Report completion
		// with the last bytes so the cache writer can publish before the client
		// receives Content-Length bytes and is free to close the connection.
		err = io.EOF
	}
	return n, err
}
func (b *sharingBufferBody) Close() error {
	b.mu.Lock()
	if b.reader == nil {
		b.mu.Unlock()
		return nil
	}
	b.reader = nil
	release := b.release
	b.release = nil
	b.mu.Unlock()
	release()
	return nil
}

type sharingReplayBody struct {
	mu       sync.Mutex
	prefix   *bytes.Reader
	source   io.ReadCloser
	terminal error
	closed   bool
	once     sync.Once
	release  func()
	err      error
}

func (b *sharingReplayBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if b.prefix != nil {
		n, err := b.prefix.Read(p)
		if n != 0 {
			b.mu.Unlock()
			return n, err
		}
		b.prefix = nil
	}
	if b.terminal != nil {
		err := b.terminal
		b.terminal = nil
		b.mu.Unlock()
		return 0, err
	}
	source := b.source
	b.mu.Unlock()
	return source.Read(p)
}
func (b *sharingReplayBody) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed, b.prefix = true, nil
		source := b.source
		b.source = nil
		b.mu.Unlock()
		b.err = source.Close()
		b.release()
		b.release = nil
	})
	return b.err
}

type sharingOriginBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *sharingOriginBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

var _ io.ReadCloser = (*sharingBufferBody)(nil)
var _ io.ReadCloser = (*sharingReplayBody)(nil)
var _ io.ReadCloser = (*sharingOriginBody)(nil)
