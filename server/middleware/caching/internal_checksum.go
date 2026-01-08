package caching

import "github.com/omalloc/tavern/api/defined/v1/event"

var _ event.CacheCompleted = (*cacheCompleted)(nil)

type cacheCompleted struct {
	ratio         int
	storeUrl      string
	storeKey      string
	storePath     string
	lastModified  string
	contentLength int64
	chunkCount    int
	chunkSize     uint64
}

// ReportRatio implements event.CacheCompleted.
func (cc *cacheCompleted) ReportRatio() int {
	return cc.ratio
}

func (cc *cacheCompleted) Kind() event.Kind {
	return "cache.completed"
}

func (cc *cacheCompleted) StoreUrl() string {
	return cc.storeUrl
}

func (cc *cacheCompleted) StorePath() string {
	return cc.storePath
}

func (cc *cacheCompleted) StoreKey() string {
	return cc.storeKey
}

func (cc *cacheCompleted) ContentLength() int64 {
	return cc.contentLength
}

func (cc *cacheCompleted) LastModified() string {
	return cc.lastModified
}

func (cc *cacheCompleted) ChunkCount() int {
	return cc.chunkCount
}

func (cc *cacheCompleted) ChunkSize() uint64 {
	return cc.chunkSize
}
