package caching

import (
	"testing"

	"github.com/kelindar/bitmap"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage/bucket/empty"
	"github.com/omalloc/tavern/storage/sharedkv"
	"github.com/stretchr/testify/assert"
)

func mockProcessorChain() *ProcessorChain {
	return &ProcessorChain{}
}

func Test_getContents(t *testing.T) {
	emptyBucket, _ := empty.New(&conf.Bucket{Path: "/tmp/tavern_test"}, sharedkv.NewEmpty())

	objectID := object.NewID("http://www.example.com/path/to/1.apk")
	c := &Caching{
		log:       log.NewHelper(log.GetLogger()),
		processor: mockProcessorChain(),
		id:        objectID,
		opt: &cachingOption{
			SliceSize: 524288,
		},
		md: &object.Metadata{
			ID:        objectID,
			BlockSize: 1048576, // 1MB 块大小
			Chunks:    bitmap.Bitmap{},
		},
		bucket: emptyBucket,
	}

	// 模拟已有的块：0, 2
	c.md.Chunks.Set(0)
	c.md.Chunks.Set(2)

	reqChunks := []uint32{0, 1, 2}

	for i := 0; i < len(reqChunks); {
		_, count, err := getContents(c, reqChunks, uint32(i))
		assert.NoError(t, err)

		if count == 0 {
			break
		}

		t.Logf("Request chunk index: %d, Retrieved chunk count: %d", reqChunks[i], count)

		i += count
	}
}
