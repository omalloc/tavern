package memory_test

import (
	"crypto/rand"
	"runtime"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/memory"
	"github.com/omalloc/tavern/storage/sharedkv"
	"github.com/stretchr/testify/assert"

	// register indexdb
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
)

func TestMemoryBucket(t *testing.T) {
	var m runtime.MemStats

	bucket, err := memory.New(&storage.BucketConfig{
		Path:   "inmemory",
		DBType: storage.TypeInMemory,
	}, sharedkv.NewMemSharedKV())

	assert.NoError(t, err)

	runtime.ReadMemStats(&m)
	t.Logf("Alloc = %v", m.Alloc)
	t.Logf("TotalAlloc = %v", m.TotalAlloc)
	t.Logf("Sys = %v", m.Sys)
	t.Logf("NumGC = %v", m.NumGC)

	t.Logf("StoreType = %s", bucket.StoreType())

	id := object.NewID("http://sendya.me.gslb.com/path/to/1.apk")
	chunkFile, wpath, err := bucket.WriteChunkFile(t.Context(), id, 0)
	if err != nil {
		t.Fatal(err)
		return
	}

	t.Logf("wpath = %s", wpath)

	buf := make([]byte, 1<<20)
	_, _ = rand.Read(buf)

	n, err := chunkFile.Write(buf)
	assert.NoError(t, err)

	assert.Equal(t, n, len(buf))
	_ = chunkFile.Close()

	runtime.ReadMemStats(&m)

	t.Logf("Alloc = %v", m.Alloc)
	t.Logf("TotalAlloc = %v", m.TotalAlloc)
	t.Logf("Sys = %v", m.Sys)
	t.Logf("NumGC = %v", m.NumGC)

}
