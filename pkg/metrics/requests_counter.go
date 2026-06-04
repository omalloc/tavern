package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// RequestsCounter tracks HTTP request counts by protocol and response code.
// It implements prometheus.Collector so Prometheus can scrape per-(protocol,code)
// counters at scrape interval, while real-time consumers call Snapshot() to get
// code-aggregated totals without walking the full metric registry.
type RequestsCounter struct {
	mu   sync.Mutex
	data map[string]map[string]uint64 // protocol -> code -> count
	desc *prometheus.Desc
}

// NewRequestsCounter creates a RequestsCounter and registers it with the given
// registerer. The produced metric name is tr_tavern_requests_code_total with
// labels "protocol" and "code", matching the previous CounterVec-based naming.
func NewRequestsCounter(opts prometheus.Opts, labelNames []string) *RequestsCounter {
	variableLabels := prometheus.UnconstrainedLabels(labelNames)
	c := &RequestsCounter{
		data: make(map[string]map[string]uint64),
		desc: prometheus.NewDesc(
			prometheus.BuildFQName(opts.Namespace, opts.Subsystem, opts.Name),
			opts.Help,
			variableLabels,
			opts.ConstLabels,
		),
	}
	return c
}

// Seed ensures the counter has a zero-valued entry for the given label pair,
// so it appears in /metrics output even before any real increment happens.
func (c *RequestsCounter) Seed(protocol, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.data[protocol] == nil {
		c.data[protocol] = make(map[string]uint64)
	}
	if _, exists := c.data[protocol][code]; !exists {
		c.data[protocol][code] = 0
	}
}

// Inc increments the counter for the given protocol and status code.
func (c *RequestsCounter) Inc(protocol, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data[protocol] == nil {
		c.data[protocol] = make(map[string]uint64)
	}
	c.data[protocol][code]++
}

// Snapshot returns the cumulative count aggregated by status code across all
// protocols. This is the fast path for real-time consumers (e.g. QS plugin)
// that need per-second reads without triggering a full Gather.
func (c *RequestsCounter) Snapshot() map[string]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]float64, len(c.data)*3)
	for _, codes := range c.data {
		for code, val := range codes {
			result[code] += float64(val)
		}
	}
	return result
}

// Describe implements prometheus.Collector.
func (c *RequestsCounter) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect implements prometheus.Collector. Each label combination is emitted
// as a separate Counter metric so the output matches the previous CounterVec.
func (c *RequestsCounter) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for proto, codes := range c.data {
		for code, val := range codes {
			ch <- prometheus.MustNewConstMetric(
				c.desc,
				prometheus.CounterValue,
				float64(val),
				proto, code,
			)
		}
	}
}
