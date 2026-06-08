package caching

import (
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// cacheRequestTotal tracks cache request outcomes by cache status and store type.
	// Labels: cache_status (HIT/MISS/PART_HIT/PART_MISS/BYPASS/REVALIDATE_HIT/REVALIDATE_MISS/HOT_HIT),
	//          store_type (disk/memory/hot/warm)
	cacheRequestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_requests_total",
		Help:      "The total number of cache requests by status and store type",
	}, []string{"cache_status", "store_type"})

	// cacheChunkWriteTotal tracks chunk write outcomes during cache fill.
	// Labels: result (success/failed), store_type
	cacheChunkWriteTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_chunk_write_total",
		Help:      "The total number of chunk write operations by result",
	}, []string{"result", "store_type"})

	// cacheFlushFailedTotal counts flush-to-disk failures that cause object discard.
	// Labels: store_type
	cacheFlushFailedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_flush_failed_total",
		Help:      "The total number of cache flush failures that triggered object discard",
	}, []string{"store_type"})

	// cacheFillrangeTotal counts how many times the fillrange path was entered
	// (upstream sub-requests to fill missing chunks). Labels: store_type
	cacheFillrangeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: pkgmetrics.Namespace,
		Name:      "cache_fillrange_total",
		Help:      "The total number of fillrange upstream sub-requests triggered by partial cache hits",
	}, []string{"store_type"})
)

func init() {
	prometheus.MustRegister(
		cacheRequestTotal,
		cacheChunkWriteTotal,
		cacheFlushFailedTotal,
		cacheFillrangeTotal,
	)
}
