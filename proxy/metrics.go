package proxy

import (
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// upstreamRequestDuration tracks upstream round-trip latency per upstream address.
	// Labels: addr
	upstreamRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "upstream_request_duration_seconds",
		Help:      "Upstream request round-trip latency histogram",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"addr"})

	// upstreamErrorsTotal counts upstream errors by upstream address and error type.
	// Labels: addr, error_type (network/timeout/http_status)
	upstreamErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "upstream_errors_total",
		Help:      "The total number of upstream request errors by upstream address and error type",
	}, []string{"addr", "error_type"})

	// collapseRequestsTotal tracks singleflight request coalescing outcomes.
	// Labels: result (primary/shared)
	collapseRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "collapse_requests_total",
		Help:      "The total number of singleflight-collapsed upstream requests",
	}, []string{"result"})
)

func init() {
	prometheus.MustRegister(
		upstreamRequestDuration,
		upstreamErrorsTotal,
		collapseRequestsTotal,
	)
}
