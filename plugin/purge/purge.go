package purge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
	"github.com/omalloc/tavern/pkg/encoding"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/plugin"
	"github.com/omalloc/tavern/storage"
)

const Method = "PURGE"
const PurgeKeyPrefix = "purge/"

var _ configv1.Plugin = (*PurgePlugin)(nil)

type option struct {
	Threshold  int      `json:"threshold" yaml:"threshold"`
	AllowHosts []string `json:"allow_hosts" yaml:"allow_hosts"`
	HeaderName string   `json:"header_name" yaml:"header_name"` // default `Purge-Type`
	LogPath    string   `json:"log_path" yaml:"log_path"`
}

type PurgePlugin struct {
	log       *log.Helper
	opt       *option
	allowAddr map[string]struct{}
}

func init() {
	plugin.Register("purge", NewPurgePlugin)
}

func (r *PurgePlugin) Start(ctx context.Context) error {
	return nil
}

func (r *PurgePlugin) Stop(ctx context.Context) error {
	return nil
}

func (r *PurgePlugin) AddRouter(router *http.ServeMux) {

	codec := encoding.GetDefaultCodec()
	sharedkv := storage.Current().SharedKV()

	router.Handle("/plugin/purge/tasks", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// query sharedkv purge task list

		purgeTaskMap := make(map[string]uint64)

		sharedkv.IteratePrefix(req.Context(), []byte("dir/"), func(key, val []byte) error {
			purgeTaskMap[string(key)[4:]] = binary.LittleEndian.Uint64(val)
			return nil
		})

		// marshal to json
		payload, err := codec.Marshal(purgeTaskMap)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Device-Plugin", "purger")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
}

func (r *PurgePlugin) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != Method {
			next(w, req)
			return
		}
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			host = req.RemoteAddr
		}
		// Darwin may report an unnamed Unix peer as empty rather than "@".
		// Normalize only a listener identity supplied by net/http, never a
		// forwarded header; the explicit "@" allowlist entry is still required.
		if local, ok := req.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && local.Network() == "unix" {
			host = "@"
		}
		fail := func(status int) {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(status)
			_metricPurgeRequestsTotal.WithLabelValues(strconv.Itoa(status)).Inc()
		}
		if _, allowed := r.allowAddr[host]; !allowed {
			fail(http.StatusForbidden)
			return
		}
		control, err := parsePurgeControl(req.Header.Get(r.opt.HeaderName))
		if err != nil {
			fail(http.StatusBadRequest)
			return
		}
		target := req.Header.Get(protocol.InternalStoreUrl)
		if target == "" {
			u := *req.URL
			if u.Host == "" {
				u.Host = req.Host
			}
			if u.Scheme == "" {
				u.Scheme = xhttp.Scheme(req)
			}
			target = u.String()
		}
		key, err := object.NewResourceKey(target, "purge", "")
		if err != nil {
			fail(http.StatusBadRequest)
			return
		}
		target = key.CanonicalURL()
		err = storage.Current().PURGE(target, control)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, storagev1.ErrKeyNotFound):
				status = http.StatusNotFound
			case errors.Is(err, storagev1.ErrInvalidPurge):
				status = http.StatusBadRequest
			case errors.Is(err, storagev1.ErrInvalidationBudget), errors.Is(err, storagev1.ErrControlUnsupported), errors.Is(err, storagev1.ErrDurabilityUnsupported):
				status = http.StatusServiceUnavailable
			}
			r.log.Errorf("purge %s failed: %v", target, err)
			fail(status)
			return
		}
		payload := []byte(`{"message":"success"}`)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		_metricPurgeRequestsTotal.WithLabelValues("200").Inc()
	}
}

func NewPurgePlugin(opts configv1.Option, log *log.Helper) (configv1.Plugin, error) {
	opt := &option{
		HeaderName: "Purge-Type",
	}
	if err := opts.Unmarshal(opt); err != nil {
		return nil, err
	}

	allowAddr := make(map[string]struct{}, len(opt.AllowHosts))
	for _, addr := range opt.AllowHosts {
		allowAddr[addr] = struct{}{}
	}

	return &PurgePlugin{
		log:       log,
		opt:       opt,
		allowAddr: allowAddr,
	}, nil
}

func parsePurgeControl(value string) (storagev1.PurgeControl, error) {
	parts := strings.Split(strings.ToLower(value), ",")
	control := storagev1.PurgeControl{MarkExpired: true}
	if len(parts) > 2 {
		return control, storagev1.ErrInvalidPurge
	}
	switch strings.TrimSpace(parts[0]) {
	case "", "file":
	case "dir":
		control.Dir = true
	default:
		return control, storagev1.ErrInvalidPurge
	}
	if len(parts) == 2 {
		switch strings.TrimSpace(parts[1]) {
		case "soft", "mark_expired":
		case "hard":
			control.Hard = true
			control.MarkExpired = false
		default:
			return control, storagev1.ErrInvalidPurge
		}
	}
	return control, nil
}
