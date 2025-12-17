package purge

import (
	"context"
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
	AllowAddr  []string `json:"allow-addr" yaml:"allow-addr"`
	HeaderName string   `yaml:"header-name"` // default `Purge-Type`
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

		ipport := strings.Split(req.RemoteAddr, ":")
		if _, ok := r.allowAddr[ipport[0]]; !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// TODO: generate store-url and delete index
		storeUrl := req.Header.Get(constants.InternalStoreUrl)
		if storeUrl == "" {
			storeUrl = req.URL.String()
		}
		r.log.Infof("purge request %s received: %s", ipport[0], storeUrl)

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

	allowAddr := make(map[string]struct{}, len(opt.AllowAddr))
	for _, addr := range opt.AllowAddr {
		allowAddr[addr] = struct{}{}
	}

	return &PurgePlugin{
		log:       log,
		opt:       opt,
		allowAddr: allowAddr,
	}, nil
}
