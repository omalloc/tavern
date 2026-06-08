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

	// requestDuration tracks end-to-end request latency by HTTP method.
	requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "request_duration_seconds",
		Help:      "End-to-end request latency histogram by HTTP method",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"method"})

	// connectionsActive tracks the current number of active client connections.
	connectionsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "connections_active",
		Help:      "The current number of active client connections",
	})
)

func init() {
	prometheus.MustRegister(
		_metricRequestCodeCounterTotal,
		_metricRequestUnexpectedClosedTotal,
		requestDuration,
		connectionsActive,
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
