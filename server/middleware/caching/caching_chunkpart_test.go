package caching

import (
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

func makebuf(size int) []byte {
	buf := make([]byte, size)
	_, _ = rand.Read(buf)
	return buf
}

func mockStoreFiles(basepath string, c *Caching, indexes ...uint32) {
	_ = os.MkdirAll(filepath.Dir(c.id.WPathSlice(basepath, 0)), 0o755)

	for _, index := range indexes {
		mbytes := makebuf(1048576)
		_ = os.WriteFile(c.id.WPathSlice(basepath, index), mbytes, 0o755)
	}
}

func Test_getContents(t *testing.T) {
	basepath := t.TempDir()

	emptyBucket, _ := empty.New(&conf.Bucket{Path: basepath}, sharedkv.NewEmpty())

	req, _ := http.NewRequest(http.MethodGet, "http://www.example.com/path/to/1.apk", nil)
	objectID, _ := newObjectIDFromRequest(req, "", true)
	c := &Caching{
		log:       log.NewHelper(log.GetLogger()),
		processor: mockProcessorChain(),
		id:        objectID,
		req:       req,
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
	mockStoreFiles(basepath, c, 0, 2)

	reqChunks := []uint32{1, 2}

	readers := make([]io.ReadCloser, 0, len(reqChunks))
	for i := 0; i < len(reqChunks); {
		reader, count, err := getContents(c, reqChunks, uint32(i))
		assert.NoError(t, err)

		if count == -1 {
			break
		}

		readers = append(readers, reader)
		i += count
	}

	t.Logf("all readers %d", len(readers))

	// 缓存 0，2
	// 请求 1，2
	// MISS chunk1, HIT chunk2
	// 因找到首个 chunk1 时，会找到最近的一个 HIT chunk,并拼接成一个流
	// 所以最终会返回一个流，包含 chunk1 和 chunk2 的数据
	assert.Equal(t, 1, len(readers))
}
