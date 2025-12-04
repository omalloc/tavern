package server

import "github.com/prometheus/client_golang/prometheus"

// dummy file to make sure server/metrics.go is included in the build
var (
	// tr_tavern_requests_code_total{protocol="http",code="200"} 11111
	_metricRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "tr",
		Subsystem: "tavern",
		Name:      "requests_code_total",
		Help:      "The total number of processed requests",
	}, []string{"protocol", "code"})
	_metricRequestUnexpectedClosed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "tr",
		Subsystem: "tavern",
		Name:      "requests_unexpected_closed",
		Help:      "The total number of unexpected closed requests",
	}, []string{"protocol", "method"})
	_metricDiskUsage = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "tr",
		Subsystem: "tavern",
		Name:      "disk_usage",
		Help:      "The disk usage of the server",
	}, []string{"dev", "path"})
	_metricDiskIO = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "tr",
		Subsystem: "tavern",
		Name:      "disk_io",
		Help:      "The disk io of the server",
	}, []string{"dev", "path"})
)

func init() {
	// register metrics
	prometheus.MustRegister(_metricRequestsTotal)
	prometheus.MustRegister(_metricRequestUnexpectedClosed)
	prometheus.MustRegister(_metricDiskUsage)
	prometheus.MustRegister(_metricDiskIO)

	// init metrics
	_metricRequestsTotal.WithLabelValues("HTTP/1.1", "200")
	_metricRequestsTotal.WithLabelValues("HTTP/1.1", "206")
	_metricRequestsTotal.WithLabelValues("HTTP/1.1", "400")
	_metricRequestsTotal.WithLabelValues("HTTP/1.1", "404")
	_metricRequestsTotal.WithLabelValues("HTTP/1.1", "500")

	_metricRequestUnexpectedClosed.WithLabelValues("HTTP/1.1", "GET")
	_metricRequestUnexpectedClosed.WithLabelValues("HTTP/1.1", "HEAD")
}
