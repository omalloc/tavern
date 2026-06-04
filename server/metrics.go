package server

import (
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	_metricRequestCodeCounterTotal = pkgmetrics.NewRequestsCounter(prometheus.Opts{
		Namespace: pkgmetrics.Namespace,
		Name:      "requests_code_total",
		Help:      "The total number of processed requests",
	}, []string{"protocol", "code"})

	_metricRequestUnexpectedClosedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "requests_unexpected_closed_total",
		Help:      "The total number of unexpected closed requests",
	}, []string{"protocol", "method"})
)

func init() {
	prometheus.MustRegister(
		_metricRequestCodeCounterTotal,
		_metricRequestUnexpectedClosedTotal,
	)

	_metricRequestUnexpectedClosedTotal.WithLabelValues("HTTP/1.1", "GET")
	_metricRequestUnexpectedClosedTotal.WithLabelValues("HTTP/1.1", "HEAD")

	// Seed _metricRequestCodeCounterTotal so common label pairs appear in
	// /metrics even before any real request increments them.
	for _, code := range []string{"200", "206", "302", "304", "404", "500"} {
		_metricRequestCodeCounterTotal.Seed("HTTP/1.1", code)
	}
}

// SetRequestsCounter sets the shared requests counter. Must be called before
// the server starts serving requests (typically from main.init).
func SetRequestsCounter(rc *pkgmetrics.RequestsCounter) {
	_metricRequestCodeCounterTotal = rc
}

// RequestsCounter returns the shared requests counter for external packages.
func RequestsCounter() *pkgmetrics.RequestsCounter {
	return _metricRequestCodeCounterTotal
}
