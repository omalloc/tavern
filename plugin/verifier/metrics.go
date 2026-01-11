package verifier

import "github.com/prometheus/client_golang/prometheus"

var (
	// Labels http.StatusCode  if code is 0 means network problem.
	//	e.g. 200, 400, 500 ...
	_metricVerifierRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "tr",
		Subsystem: "tavern",
		Name:      "verifier_requests_total",
		Help:      "Total number of verifier reports",
	}, []string{"code"})
)

func init() {
	prometheus.MustRegister(_metricVerifierRequestsTotal)

	_metricVerifierRequestsTotal.WithLabelValues("409")
	_metricVerifierRequestsTotal.WithLabelValues("200")
	_metricVerifierRequestsTotal.WithLabelValues("0")
}
