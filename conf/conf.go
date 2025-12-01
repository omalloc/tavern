package conf

import (
	"time"

	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/pkg/mapstruct"
)

type Bootstrap struct {
	Strict   bool      `json:"strict" yaml:"strict"`
	PidFile  string    `json:"pidfile" yaml:"pidfile"`
	Server   *Server   `json:"server" yaml:"server"`
	Plugin   []*Plugin `json:"plugin" yaml:"plugin"`
	Upstream *Upstream `json:"upstream" yaml:"upstream"`
}

type Server struct {
	Addr              string                     `json:"addr" yaml:"addr"`
	ReadTimeout       time.Duration              `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout      time.Duration              `json:"write_timeout" yaml:"write_timeout"`
	IdleTimeout       time.Duration              `json:"idle_timeout" yaml:"idle_timeout"`
	ReadHeaderTimeout time.Duration              `json:"read_header_timeout" yaml:"read_header_timeout"`
	MaxHeaderBytes    int                        `json:"max_header_bytes" yaml:"max_header_bytes"`
	Middleware        []*middlewarev1.Middleware `json:"middleware" yaml:"middleware"`
}

type Upstream struct {
	Balancing           string   `json:"balancing" yaml:"balancing"`
	Address             []string `json:"address" yaml:"address"`
	MaxIdleConns        int      `json:"max_idle_conns" yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int      `json:"max_idle_conns_per_host" yaml:"max_idle_conns_per_host"`
	MaxConnsPerServer   int      `json:"max_conns_per_server" yaml:"max_conns_per_server"`
	InsecureSkipVerify  bool     `json:"insecure_skip_verify" yaml:"insecure_skip_verify"`
	ResolveAddresses    bool     `json:"resolve_addresses" yaml:"resolve_addresses"`
	Features            []string `json:"features" yaml:"features"`
}

type Storage struct {
	Driver          string    `json:"driver" yaml:"driver"`
	AsyncLoad       bool      `json:"async_load" yaml:"async_load"`
	EvictionPolicy  string    `json:"eviction_policy" yaml:"eviction_policy"`
	SelectionPolicy string    `json:"selection_policy" yaml:"selection_policy"`
	Buckets         []*Bucket `json:"buckets" yaml:"buckets"`
}

type Bucket struct {
	Path string `json:"path" yaml:"path"`
	Type string `json:"type" yaml:"type"`
}

type Plugin struct {
	Name    string         `json:"name" yaml:"name"`
	Options map[string]any `json:"options" yaml:"options"`
}

func (r *Plugin) PluginName() string {
	return r.Name
}

func (r *Plugin) Unmarshal(v any) error {
	return mapstruct.Decode(r, v)
}
