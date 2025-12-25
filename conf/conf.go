package conf

import (
	"time"

	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/pkg/mapstruct"
)

type Bootstrap struct {
	Strict   bool      `json:"strict" yaml:"strict"`
	PidFile  string    `json:"pidfile" yaml:"pidfile"`
	Logger   *Logger   `json:"logger" yaml:"logger"`
	Server   *Server   `json:"server" yaml:"server"`
	Plugin   []*Plugin `json:"plugin" yaml:"plugin"`
	Upstream *Upstream `json:"upstream" yaml:"upstream"`
	Storage  *Storage  `json:"storage" yaml:"storage"`
}

type Logger struct {
	Level   string `json:"level" yaml:"level"`
	Path    string `json:"path" yaml:"path"`
	Caller  bool   `json:"caller" yaml:"caller"`
	TraceID bool   `json:"traceid" yaml:"traceid"`
	NoPid   bool   `json:"nopid" yaml:"nopid"`
}

type Server struct {
	Addr              string                     `json:"addr" yaml:"addr"`
	ReadTimeout       time.Duration              `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout      time.Duration              `json:"write_timeout" yaml:"write_timeout"`
	IdleTimeout       time.Duration              `json:"idle_timeout" yaml:"idle_timeout"`
	ReadHeaderTimeout time.Duration              `json:"read_header_timeout" yaml:"read_header_timeout"`
	MaxHeaderBytes    int                        `json:"max_header_bytes" yaml:"max_header_bytes"`
	Middleware        []*middlewarev1.Middleware `json:"middleware" yaml:"middleware"`
	PProf             *ServerPProf               `json:"pprof" yaml:"pprof"`
	AccessLog         *ServerAccessLog           `json:"access_log" yaml:"access_log"`
}

type ServerPProf struct {
	Username string `json:"username" yaml:"username"`
	Password string `json:"password" yaml:"password"`
}

type ServerAccessLog struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Path    string `json:"path" yaml:"path"`
	Encrypt struct {
		Enabled bool   `json:"enabled" yaml:"enabled"`
		Secret  string `json:"secret" yaml:"secret"`
	} `json:"encrypt" yaml:"encrypt"`
}

type Upstream struct {
	Balancing           string         `json:"balancing" yaml:"balancing"`
	Address             []string       `json:"address" yaml:"address"`
	MaxIdleConns        int            `json:"max_idle_conns" yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int            `json:"max_idle_conns_per_host" yaml:"max_idle_conns_per_host"`
	MaxConnsPerServer   int            `json:"max_conns_per_server" yaml:"max_conns_per_server"`
	InsecureSkipVerify  bool           `json:"insecure_skip_verify" yaml:"insecure_skip_verify"`
	ResolveAddresses    bool           `json:"resolve_addresses" yaml:"resolve_addresses"`
	Features            map[string]any `json:"features" yaml:"features"`
}

type Storage struct {
	Driver          string    `json:"driver" yaml:"driver"`
	DBType          string    `json:"db_type" yaml:"db_type"`
	AsyncLoad       bool      `json:"async_load" yaml:"async_load"`
	EvictionPolicy  string    `json:"eviction_policy" yaml:"eviction_policy"`
	SelectionPolicy string    `json:"selection_policy" yaml:"selection_policy"`
	SliceSize       uint64    `json:"slice_size" yaml:"slice_size"`
	Buckets         []*Bucket `json:"buckets" yaml:"buckets"`
}

type Bucket struct {
	Path      string `json:"path" yaml:"path"`             // local path or ?
	Driver    string `json:"driver" yaml:"driver"`         // native, custom-driver
	Type      string `json:"type" yaml:"type"`             // normal, cold, hot, fastmemory
	DBType    string `json:"db_type" yaml:"db_type"`       // boltdb, badgerdb, pebble
	AsyncLoad bool   `json:"async_load" yaml:"async_load"` // load metadata async
	SliceSize uint64 `json:"slice_size" yaml:"slice_size"` // slice size for each part
}

type Plugin struct {
	Name    string         `json:"name" yaml:"name"`
	Options map[string]any `json:"options" yaml:"options"`
}

func (r *Plugin) PluginName() string {
	return r.Name
}

func (r *Plugin) Unmarshal(v any) error {
	return mapstruct.Decode(r.Options, v)
}
