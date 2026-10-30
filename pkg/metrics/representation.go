package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

var (
	varyLookup               = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "vary_lookup_total", Help: "Representation selection decisions; excludes admission and internal verification reads."}, []string{"result", "reason"})
	varyAdmission            = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "vary_admission_total", Help: "Representation response admission decisions; admission does not imply successful publication."}, []string{"result", "reason"})
	representationCommit     = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "representation_commit_total", Help: "Paired representation publication outcomes, including metadata-only updates."}, []string{"result"})
	representationPending    = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: "representation_pending", Help: "Unreleased selector leases, including superseded writers until their actual cleanup."}, []string{"kind"})
	representationInvalidate = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "representation_invalidate_total", Help: "Manager invalidation calls, including mapped source barriers; not client request counts."}, []string{"mode", "scope", "result"})
	sourceFetch              = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "source_fetch_total", Help: "Managed source resolutions: hit, validating 304, full miss/replacement or failure."}, []string{"result"})
	transformTotal           = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "transform_total", Help: "Actual managed conversion attempts through output verification/publication; excludes hits and startup probes."}, []string{"backend", "source_mime", "target_mime", "result"})
	transformDuration        = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: Namespace, Name: "transform_duration_seconds", Help: "Managed conversion time from backend invocation through verification/publication; excludes source acquisition and startup probes.", Buckets: prometheus.DefBuckets}, []string{"backend", "result"})
	transformQueue           = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: "transform_queue_depth", Help: "Waiting tasks at each managed pipeline or worker budget; not accumulated submissions."}, []string{"backend", "stage"})
	transformActive          = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: Namespace, Name: "transform_active", Help: "Acquired permits, including unconsumed private or worker outputs until Close."}, []string{"backend", "stage"})
	transformRejected        = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "transform_queue_rejected_total", Help: "Budget admission rejected by queue capacity, timeout or cancellation."}, []string{"backend", "stage", "reason"})
	representationGC         = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "representation_gc_total", Help: "GC decision events: collected candidate, skipped/protected record, or failed scan/deletion."}, []string{"result"})
)

var barrierCompaction = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "representation_barrier_compaction_total", Help: "Barrier maintenance decisions; removed/retained candidates and failed passes."}, []string{"result"})
var legacyCollection = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "legacy_vary_gc_total", Help: "Retired legacy Vary maintenance decisions; collected/skipped candidates and failed passes."}, []string{"result"})
var legacyFileOps = prometheus.NewCounter(prometheus.CounterOpts{Namespace: Namespace, Name: "legacy_vary_gc_file_ops_total", Help: "Budgeted final/temporary file removal attempts by legacy Vary maintenance."})
var responseSharing = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: Namespace, Name: "response_sharing_total", Help: "One decision per optional verified whole-response sharing invocation."}, []string{"result"})
var responseSharingActive = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "response_sharing_active", Help: "Reserved response buffers, retained until the last body closes."})
var responseSharingWaiters = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: Namespace, Name: "response_sharing_waiters", Help: "Requests currently waiting for response reuse proof and complete bytes."})

func init() {
	tavernRegistry.MustRegister(barrierCompaction, legacyCollection, legacyFileOps, varyLookup, varyAdmission, representationCommit, representationPending, representationInvalidate, sourceFetch, transformTotal, transformDuration, transformQueue, transformActive, transformRejected, representationGC)
	tavernRegistry.MustRegister(responseSharing, responseSharingActive, responseSharingWaiters)
}

func RecordResponseSharing(result string) {
	responseSharing.WithLabelValues(fixedLabel(result, "leader", "shared", "rejected", "capacity", "timeout", "cancelled", "bypass")).Inc()
}
func ResponseSharingActive(delta float64)  { responseSharingActive.Add(delta) }
func ResponseSharingWaiters(delta float64) { responseSharingWaiters.Add(delta) }

// All caller-supplied labels pass fixed allowlists, including test backends and
// error paths. Neither a raw URL nor an arbitrary encoder/spec/version can grow
// series cardinality or enter a scrape through these APIs.
func fixedLabel(value string, values ...string) string {
	for _, allowed := range values {
		if value == allowed {
			return value
		}
	}
	return "other"
}
func backendLabel(backend string) string { return fixedLabel(backend, "http", "other") }
func mimeLabel(mime string) string {
	return fixedLabel(mime, "image/jpeg", "image/png", "image/webp", "other")
}
func RecordVaryLookup(result, reason string) {
	varyLookup.WithLabelValues(fixedLabel(result, "hit", "miss", "bypass"), fixedLabel(reason, "matched", "not_found", "stale", "range", "request_policy", "conditions", "body", "policy", "control_error", "legacy")).Inc()
}
func RecordVaryAdmission(result, reason string) {
	varyAdmission.WithLabelValues(fixedLabel(result, "admitted", "bypass"), fixedLabel(reason, "eligible", "wildcard", "invalid_vary", "field_budget", "selector_budget", "policy", "shape", "control_error", "legacy")).Inc()
}
func RecordRepresentationCommit(published bool, failed bool) {
	result := "superseded"
	if failed {
		result = "failed"
	} else if published {
		result = "published"
	}
	representationCommit.WithLabelValues(result).Inc()
}
func RepresentationPending(kind string, delta float64) {
	representationPending.WithLabelValues(fixedLabel(kind, "origin", "derived")).Add(delta)
}
func RecordRepresentationInvalidation(hard, dir, failed bool) {
	mode, scope, result := "soft", "file", "success"
	if hard {
		mode = "hard"
	}
	if dir {
		scope = "dir"
	}
	if failed {
		result = "failed"
	}
	representationInvalidate.WithLabelValues(mode, scope, result).Inc()
}
func RecordSourceFetch(result string) {
	sourceFetch.WithLabelValues(fixedLabel(result, "hit", "revalidate", "miss", "failed")).Inc()
}
func RecordTransform(backend, source, target, result string, duration time.Duration) {
	backend = backendLabel(backend)
	result = fixedLabel(result, "success", "failed", "timeout", "cancelled", "superseded")
	transformTotal.WithLabelValues(backend, mimeLabel(source), mimeLabel(target), result).Inc()
	transformDuration.WithLabelValues(backend, result).Observe(max(0, duration.Seconds()))
}
func TransformBudget(backend, stage string, queued, active float64) {
	backend = backendLabel(backend)
	stage = fixedLabel(stage, "task", "worker")
	transformQueue.WithLabelValues(backend, stage).Add(queued)
	transformActive.WithLabelValues(backend, stage).Add(active)
}
func RecordTransformRejected(backend, stage, reason string) {
	transformRejected.WithLabelValues(backendLabel(backend), fixedLabel(stage, "task", "worker"), fixedLabel(reason, "full", "timeout", "cancelled")).Inc()
}
func RecordRepresentationGC(result string) {
	representationGC.WithLabelValues(fixedLabel(result, "collected", "skipped", "failed")).Inc()
}

func RecordBarrierCompaction(scanned, removed int, failed bool) {
	barrierCompaction.WithLabelValues("removed").Add(float64(removed))
	barrierCompaction.WithLabelValues("retained").Add(float64(max(0, scanned-removed)))
	if failed {
		barrierCompaction.WithLabelValues("failed").Inc()
	}
}
func RecordLegacyCollection(collected, skipped, files int, failed bool) {
	legacyCollection.WithLabelValues("collected").Add(float64(collected))
	legacyCollection.WithLabelValues("skipped").Add(float64(skipped))
	legacyFileOps.Add(float64(files))
	if failed {
		legacyCollection.WithLabelValues("failed").Inc()
	}
}
