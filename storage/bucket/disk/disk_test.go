package disk_test

import (
	"context"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/disk"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
	"github.com/omalloc/tavern/storage/sharedkv"
)

func newTestBucket(t *testing.T, basepath string) storagev1.Bucket {
	bucket, err := disk.New(&storagev1.BucketConfig{
		Path:      basepath,
		Driver:    "native",
		Type:      "normal",
		DBType:    "pebble",
		DBPath:    path.Join(basepath, ".indexdb"),
		AsyncLoad: false,
	}, sharedkv.NewEmpty())

	assert.NoError(t, err)
	return bucket
}

func TestMissKey(t *testing.T) {
	basepath := t.TempDir()
	bucket := newTestBucket(t, basepath)

	cackeKey := object.NewID("http://www.example.com/path/to/1M.bin")

	t.Logf("cache-key=%s", cackeKey.HashStr())

	md, err := bucket.Lookup(context.Background(), cackeKey)

	assert.ErrorIs(t, err, storagev1.ErrKeyNotFound)

	assert.Nil(t, md)
}

func TestHitKey(t *testing.T) {
	basepath := t.TempDir()

	bucket := newTestBucket(t, basepath)

	cackeKey := object.NewID("http://www.example.com/path/to/1M.bin")

	t.Logf("cache-key=%s", cackeKey.HashStr())

	err := bucket.Store(context.Background(), &object.Metadata{
		Flags:       object.FlagCache,
		ID:          cackeKey,
		Code:        http.StatusOK,
		Size:        1,
		RespUnix:    time.Now().Unix(),
		LastRefUnix: time.Now().Unix(),
		Refs:        1,
		ExpiresAt:   time.Now().Add(time.Second * 30).Unix(),
		Headers:     make(http.Header),
	})

	assert.NoError(t, err)

	md, err := bucket.Lookup(context.Background(), cackeKey)

	assert.NoError(t, err)
	assert.NotNil(t, md)

	t.Logf("flags=%d size=%d, code=%d, refs=%d", md.Flags, md.Size, md.Code, md.Refs)

	assert.Equal(t, md.Flags, object.FlagCache)
	assert.Equal(t, md.Size, uint64(1))
	assert.Equal(t, md.Code, http.StatusOK)

	t.Logf("filepath=%s", cackeKey.WPath("/"))
}
