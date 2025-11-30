package conf

import (
	"time"

	middlewarev1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	"github.com/omalloc/tavern/pkg/mapstruct"
)

type Bootstrap struct {
	Strict  bool      `json:"strict" yaml:"strict"`
	PidFile string    `json:"pidfile" yaml:"pidfile"`
	Server  *Server   `json:"server" yaml:"server"`
	Plugin  []*Plugin `json:"plugin" yaml:"plugin"`
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
