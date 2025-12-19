package purge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	configv1 "github.com/omalloc/tavern/api/defined/v1/plugin"
	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/constants"
	"github.com/omalloc/tavern/plugin"
	"github.com/omalloc/tavern/storage"
)

const Method = "PURGE"

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
	router.Handle("/plugin/purge/tasks", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// TODO: query sharedkv purge task list

		var payload []byte

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Device-Plugin", "purger")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
}

func (r *PurgePlugin) HandleFunc(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// skip not PURGE request. e.g. curl -X PURGE http://www.example.com/
		if req.Method != Method {
			next(w, req)
			return
		}

		ipPort := strings.Split(req.RemoteAddr, ":")
		if _, ok := r.allowAddr[ipPort[0]]; !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// TODO: generate store-url and delete index
		storeUrl := req.Header.Get(constants.InternalStoreUrl)
		if storeUrl == "" {
			storeUrl = req.URL.String()
		}
		r.log.Debugf("purge request %s received: %s", ipPort[0], storeUrl)

		// purge dir
		if typ := req.Header.Get(r.opt.HeaderName); strings.ToLower(typ) == "dir" {
			// TODO: add DIR purge task.
			return
		}

		// purge single file.
		if err := storage.Current().PURGE(storeUrl, storagev1.PurgeControl{
			Hard: true,
			Dir:  false,
		}); err != nil {
			// key not found.
			if errors.Is(err, storagev1.ErrKeyNotFound) {
				w.Header().Set("Content-Length", "0")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				return
			}

			// others error
			r.log.Errorf("purge %s failed: %v", storeUrl, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		payload := []byte(`{"message":"success"}`)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
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
