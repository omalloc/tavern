package transform

import "time"

// Runtime is prepared once by server assembly. Caching validates its storage
// configuration and probes the worker before the planner can accept traffic.
type Runtime struct {
	Enabled        bool
	Router         *Router
	Worker         *HTTPWorker
	StartupTimeout time.Duration
}

func (r *Runtime) Close() {
	if r != nil && r.Worker != nil {
		r.Worker.Close()
	}
}
