package purge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage"
)

func TestPurgeUnixPeerRequiresListenerIdentityAndExplicitAllowlist(t *testing.T) {
	previous := storage.Current()
	t.Cleanup(func() { storage.SetDefault(previous) })
	for _, tc := range []struct {
		name    string
		local   net.Addr
		allowed bool
		status  int
	}{
		{"unnamed-unix", &net.UnixAddr{Name: "/tmp/test.sock", Net: "unix"}, true, 200},
		{"unix-not-allowed", &net.UnixAddr{Name: "/tmp/test.sock", Net: "unix"}, false, 403},
		{"tcp-empty-peer", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80}, true, 403},
		{"no-listener", nil, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &purgeTestStorage{}
			storage.SetDefault(store)
			allow := make(map[string]struct{})
			if tc.allowed {
				allow["@"] = struct{}{}
			}
			p := &PurgePlugin{opt: &option{HeaderName: "Purge-Type"}, log: log.NewHelper(log.DefaultLogger), allowAddr: allow}
			req := httptest.NewRequest(Method, "http://example.com/a", nil)
			req.RemoteAddr = ""
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tc.local != nil {
				req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, tc.local))
			}
			response := httptest.NewRecorder()
			p.HandleFunc(func(http.ResponseWriter, *http.Request) { t.Error("PURGE passed downstream") })(response, req)
			if response.Code != tc.status {
				t.Fatal(response.Code)
			}
			if (store.calls == 1) != (tc.status == 200) {
				t.Fatal("authorization did not precede storage", store.calls)
			}
		})
	}
}

type purgeTestStorage struct {
	storagev1.Storage
	err     error
	calls   int
	target  string
	control storagev1.PurgeControl
}

func (s *purgeTestStorage) PURGE(raw string, control storagev1.PurgeControl) error {
	s.calls++
	s.target, s.control = raw, control
	return s.err
}

func TestPurgeStatusAuthorizationAndColdDirectory(t *testing.T) {
	previous := storage.Current()
	t.Cleanup(func() { storage.SetDefault(previous) })
	for _, tc := range []struct {
		name, peer, header string
		err                error
		status             int
	}{
		{"cold-directory", "127.0.0.1:1234", "dir, hard", nil, 200},
		{"ipv6", "[::1]:1234", "file", nil, 200},
		{"forbidden", "10.0.0.1:1234", "file", nil, 403},
		{"invalid-control", "127.0.0.1:1234", "dir, unknown", nil, 400},
		{"missing-legacy", "127.0.0.1:1234", "file", storagev1.ErrKeyNotFound, 404},
		{"invalid-target", "127.0.0.1:1234", "file", storagev1.ErrInvalidPurge, 400},
		{"barrier-budget", "127.0.0.1:1234", "dir", storagev1.ErrInvalidationBudget, 503},
		{"unsupported-durability", "127.0.0.1:1234", "dir", storagev1.ErrDurabilityUnsupported, 503},
		{"sync-failure", "127.0.0.1:1234", "file", errors.New("sync failed"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &purgeTestStorage{err: tc.err}
			storage.SetDefault(store)
			p := &PurgePlugin{opt: &option{HeaderName: "Purge-Type"}, log: log.NewHelper(log.DefaultLogger), allowAddr: map[string]struct{}{"127.0.0.1": {}, "::1": {}}}
			req := httptest.NewRequest(Method, "http://EXAMPLE.com/img/", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("Purge-Type", tc.header)
			response := httptest.NewRecorder()
			p.HandleFunc(func(http.ResponseWriter, *http.Request) { t.Error("PURGE passed downstream") })(response, req)
			if response.Code != tc.status {
				t.Fatal(response.Code, response.Body.String())
			}
			if tc.status != 403 && tc.name != "invalid-control" && (store.calls != 1 || store.target != "http://example.com/img/") {
				t.Fatal("cold directory was prefiltered or target was not normalized", store.calls, store.target)
			}
			if (tc.status == 403 || tc.name == "invalid-control") && store.calls != 0 {
				t.Fatal("unauthorized/invalid purge reached storage")
			}
			if tc.name == "cold-directory" && (!store.control.Hard || !store.control.Dir) {
				t.Fatal(store.control)
			}
		})
	}
}

func TestParsePurgeControl(t *testing.T) {
	for _, value := range []string{"", "file", "dir", "dir, hard", "file,soft", "file,mark_expired"} {
		if _, err := parsePurgeControl(value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"unknown", "file,", "dir,hard,extra"} {
		if _, err := parsePurgeControl(value); err == nil {
			t.Fatal("invalid mode accepted", value)
		}
	}
}
