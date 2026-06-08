package recovery

import (
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// panicTotal counts the number of panics caught by the recovery middleware.
	panicTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "panics_total",
		Help:      "The total number of panics caught by the recovery middleware",
	})
)

func init() {
	prometheus.MustRegister(panicTotal)
}
