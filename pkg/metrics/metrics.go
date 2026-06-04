package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const Namespace = "tr_tavern"

// tavernRegistry is the isolated prometheus registry for all tavern metrics.
// It is assigned to prometheus.DefaultRegisterer in init() so that every
// prometheus.MustRegister call across all tavern packages automatically
// lands here instead of the global default.
var tavernRegistry = prometheus.NewRegistry()

func init() {
	prometheus.DefaultRegisterer = tavernRegistry
	prometheus.DefaultGatherer = tavernRegistry

	// Wrap the registry with a prefix so that all metrics are namespaced under "tr_tavern".
	wrapRegistry := prometheus.WrapRegistererWithPrefix(Namespace+"_", tavernRegistry)

	wrapRegistry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	wrapRegistry.MustRegister(collectors.NewGoCollector())
}
