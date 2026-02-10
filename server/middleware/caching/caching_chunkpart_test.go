package caching

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"testing"

	"github.com/kelindar/bitmap"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/storage/bucket/memory"
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

func Test_getContents(t *testing.T) {
	memoryBucket, _ := memory.New(&storage.BucketConfig{}, sharedkv.NewEmpty())

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://www.example.com/path/to/1.apk", nil)
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
			BlockSize: 524288,
			Chunks:    bitmap.Bitmap{},
		},
		bucket:      memoryBucket,
		proxyClient: proxy.New(),
	}

	// 模拟已有的块：0, 2
	c.md.Chunks.Set(0)
	c.md.Chunks.Set(2)

	c.md.Chunks.Range(func(x uint32) {
		f, wpath, err := memoryBucket.WriteChunkFile(context.Background(), objectID, x)
		if err != nil {
			t.Fatal(err)
		}

		t.Logf("write chunk file %s", wpath)

		buf := makebuf(1 << 19)
		_, _ = f.Write(buf)
		_ = f.Close()
	})

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
