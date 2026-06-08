package disk

import (
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// indexdbOperationDuration tracks indexdb operation latency by operation type and bucket.
	// Labels: op (get/set/delete/iterate), bucket
	indexdbOperationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "indexdb_operation_duration_seconds",
		Help:      "IndexDB operation latency histogram by operation type and bucket",
		Buckets:   []float64{.0001, .0005, .001, .005, .01, .05, .1, .5, 1},
	}, []string{"op", "bucket"})

	// diskIOBytesTotal tracks bytes read/written to disk by bucket.
	// Labels: bucket, direction (read/write)
	diskIOBytesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "disk_io_bytes_total",
		Help:      "The total number of bytes read/written to disk by bucket",
	}, []string{"bucket", "direction"})

	// cacheEvictionsTotal counts cache eviction events by bucket and reason.
	// Labels: bucket, reason (lru/demote/discard)
	cacheEvictionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_evictions_total",
		Help:      "The total number of cache evictions by bucket and reason",
	}, []string{"bucket", "reason"})

	// cacheMigrationTotal tracks object migration between storage tiers.
	// Labels: bucket, direction (promote/demote)
	cacheMigrationTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_migration_total",
		Help:      "The total number of cache object migrations between tiers",
	}, []string{"bucket", "direction"})

	// cacheObjectsGauge tracks the current number of cached objects per bucket.
	// Labels: bucket
	cacheObjectsGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_objects",
		Help:      "The current number of cached objects per bucket",
	}, []string{"bucket"})
)

func init() {
	prometheus.MustRegister(
		indexdbOperationDuration,
		diskIOBytesTotal,
		cacheEvictionsTotal,
		cacheMigrationTotal,
		cacheObjectsGauge,
	)
}
