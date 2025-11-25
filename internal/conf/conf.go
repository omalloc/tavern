package conf

import (
	"time"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
)

type Bootstrap struct {
	Strict bool    `json:"strict" yaml:"strict"`
	Server *Server `json:"server" yaml:"server"`
}

type Server struct {
	Addr              string                `json:"addr" yaml:"addr"`
	ReadTimeout       time.Duration         `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout      time.Duration         `json:"write_timeout" yaml:"write_timeout"`
	IdleTimeout       time.Duration         `json:"idle_timeout" yaml:"idle_timeout"`
	ReadHeaderTimeout time.Duration         `json:"read_header_timeout" yaml:"read_header_timeout"`
	MaxHeaderBytes    int                   `json:"max_header_bytes" yaml:"max_header_bytes"`
	Middleware        []configv1.Middleware `json:"middleware" yaml:"middleware"`
}
