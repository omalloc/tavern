package event

const CacheCompletedKey Kind = "cache.completed"

type CacheCompleted interface {
	Kind() Kind
	StoreUrl() string
	StoreKey() string
	StorePath() string
	ContentLength() int64
	LastModified() string
	ChunkCount() int
}
