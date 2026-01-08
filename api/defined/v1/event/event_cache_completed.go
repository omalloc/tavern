package event

// CacheCompletedKey marks the async signal topic emitted after a cache write
// finishes so downstream consumers can pick up metadata about the stored
// object.
const CacheCompletedKey Kind = "cache.completed"

// CacheCompleted describes the payload carried with CacheCompletedKey events.
// Implementations wrap storage metadata that observers (metrics, audit logs,
// cache plugins) need to react to a completed cache fill.
type CacheCompleted interface {
	// Kind returns the topic identifier so the payload can be routed on the
	// event bus without additional type assertions.
	Kind() Kind
	// StoreUrl is the original resource location (typically HTTP URL) that
	// produced the cached artifact.
	StoreUrl() string
	// StoreKey provides the canonical cache key (namespace + object id) used
	// by the storage backend.
	StoreKey() string
	// StorePath reports the physical path or object prefix where the data is
	// persisted, which downstream storage inspectors can use for lookups.
	StorePath() string
	// ContentLength is the total payload size so quota and metrics pipelines
	// can update usage counters without re-reading the object.
	ContentLength() int64
	// LastModified exposes the origin server's last-modified value, enabling
	// cache validators to rebuild conditional requests.
	LastModified() string
	// ChunkCount tells listeners how many blocks compose the cached file; it
	// aligns with ChunkSize for streaming or verification logic.
	ChunkCount() int
	// ChunkSize is the uniform block size used while persisting the object.
	ChunkSize() uint64
	// ReportRatio specifies the sampling ratio [-1~100];
	//	-1 disables reports,
	//	0 use default ratio defined by verifier plugin config
	//	100 reports every completion, other values represent percentage-based
	//	sampling for noisy channels.
	ReportRatio() int
}
