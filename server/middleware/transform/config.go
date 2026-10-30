package transform

import (
	"fmt"
	"time"

	configv1 "github.com/omalloc/tavern/api/defined/v1/middleware"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	planning "github.com/omalloc/tavern/transform"
)

type routeOption struct {
	PathPrefix         string `json:"path_prefix" yaml:"path_prefix"`
	Mode               string `json:"mode" yaml:"mode"`
	FormatParameter    string `json:"format_parameter" yaml:"format_parameter"`
	QualityParameter   string `json:"quality_parameter" yaml:"quality_parameter"`
	DefaultFormat      string `json:"default_format" yaml:"default_format"`
	FallbackToSource   bool   `json:"fallback_to_source" yaml:"fallback_to_source"`
	transformv1.Policy `json:",squash" yaml:",inline"`
}
type backendOption struct {
	Name         string                          `json:"name" yaml:"name"`
	Endpoint     string                          `json:"endpoint" yaml:"endpoint"`
	Capabilities transformv1.BackendCapabilities `json:"capabilities" yaml:"capabilities"`
}
type limitsOption struct {
	MaxInputBytes      int64  `json:"max_input_bytes" yaml:"max_input_bytes"`
	MaxOutputBytes     int64  `json:"max_output_bytes" yaml:"max_output_bytes"`
	MaxPixels          int64  `json:"max_pixels" yaml:"max_pixels"`
	Concurrency        int    `json:"concurrency" yaml:"concurrency"`
	QueueSize          int    `json:"queue_size" yaml:"queue_size"`
	Timeout            string `json:"timeout" yaml:"timeout"`
	TemporaryDirectory string `json:"temporary_directory" yaml:"temporary_directory"`
}
type option struct {
	Enabled        bool          `json:"enabled" yaml:"enabled"`
	Routes         []routeOption `json:"routes" yaml:"routes"`
	Backend        backendOption `json:"backend" yaml:"backend"`
	Limits         limitsOption  `json:"limits" yaml:"limits"`
	StartupTimeout string        `json:"startup_timeout" yaml:"startup_timeout"`
}

// Prepare validates local configuration without contacting the worker. Server
// assembly first validates the caching/storage binding, then runs the matrix gate.
func Prepare(config *configv1.Middleware) (*planning.Runtime, error) {
	var opt option
	if err := config.Unmarshal(&opt); err != nil {
		return nil, err
	}
	if !opt.Enabled {
		if len(opt.Routes) == 0 {
			return nil, nil
		}
		router, err := prepareRouter(opt)
		if err != nil {
			return nil, err
		}
		return &planning.Runtime{Router: router}, nil
	}
	if opt.Backend.Name != "http" || len(opt.Routes) == 0 {
		return nil, fmt.Errorf("transform requires http backend and at least one route")
	}
	timeout, err := time.ParseDuration(opt.Limits.Timeout)
	if err != nil {
		return nil, fmt.Errorf("transform limits.timeout: %w", err)
	}
	startup := 2 * time.Minute
	if opt.StartupTimeout != "" {
		startup, err = time.ParseDuration(opt.StartupTimeout)
		if err != nil || startup <= 0 {
			return nil, fmt.Errorf("transform startup_timeout must be positive")
		}
	}
	limits := planning.WorkerLimits{MaxInputBytes: opt.Limits.MaxInputBytes, MaxOutputBytes: opt.Limits.MaxOutputBytes, MaxPixels: opt.Limits.MaxPixels, Concurrency: opt.Limits.Concurrency, QueueSize: opt.Limits.QueueSize, Timeout: timeout, TemporaryDirectory: opt.Limits.TemporaryDirectory}
	worker, err := planning.NewHTTPWorker(planning.HTTPWorkerConfig{Endpoint: opt.Backend.Endpoint, Capabilities: opt.Backend.Capabilities, Limits: limits}, nil)
	if err != nil {
		return nil, err
	}
	router, err := prepareRouter(opt)
	if err != nil {
		worker.Close()
		return nil, err
	}
	return &planning.Runtime{Enabled: true, Router: router, Worker: worker, StartupTimeout: startup}, nil
}

func prepareRouter(opt option) (*planning.Router, error) {
	routes := make([]planning.Route, 0, len(opt.Routes))
	for _, route := range opt.Routes {
		if route.DefaultFormat != "" && route.DefaultFormat != "source" {
			return nil, fmt.Errorf("transform default_format must be source")
		}
		routes = append(routes, planning.Route{PathPrefix: route.PathPrefix, Mode: route.Mode, FormatParameter: route.FormatParameter, QualityParameter: route.QualityParameter, FallbackToSource: route.FallbackToSource, Policy: route.Policy})
	}
	return planning.NewRouter(routes, opt.Backend.Capabilities)
}
