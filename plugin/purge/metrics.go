package purge

import (
	"github.com/prometheus/client_golang/prometheus"

	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	_ "github.com/omalloc/tavern/pkg/metrics" // ensures DefaultRegisterer is replaced before our init
)

var (
	_metricPurgeRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "purge_requests_total",
		Help:      "Total number of purge requests",
	}, []string{"code"})
)

func init() {
	prometheus.MustRegister(_metricPurgeRequestsTotal)

	// docs/purge.md references these labels
	_metricPurgeRequestsTotal.WithLabelValues("200")
	_metricPurgeRequestsTotal.WithLabelValues("403")
	_metricPurgeRequestsTotal.WithLabelValues("404")
	_metricPurgeRequestsTotal.WithLabelValues("500")
}
