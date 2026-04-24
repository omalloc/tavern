//go:build linux
// +build linux

package rawdisk

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createSparseFile creates a sparse file of the given size for testing.
func createSparseFile(t *testing.T, size int64) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.img")
	f, err := os.Create(path)
	require.NoError(t, err)
	err = f.Truncate(size)
	require.NoError(t, err)
	f.Close()
	return path
}

func TestRawDiskBucket_BasicOps(t *testing.T) {
	// Create a 2GB sparse file to simulate a block device
	devPath := createSparseFile(t, 2*1024*1024*1024)

	bucket, err := NewBucket("test-bucket", 100, 100, devPath)
	require.NoError(t, err)
	defer bucket.Close()

	ctx := context.Background()
	objID := object.NewID("http://example.com/test.jpg")

	// 1. Write Data
	w, _, err := bucket.WriteChunkFile(ctx, objID, 0)
	require.NoError(t, err)

	data := make([]byte, 1024*100) // 100KB
	_, err = rand.Read(data)
	require.NoError(t, err)

	n, err := w.Write(data)
	assert.NoError(t, err)
	assert.Equal(t, len(data), n)

	err = w.Close()
	require.NoError(t, err)

	// Verify metadata in memory
	md, err := bucket.Lookup(ctx, objID)
	require.NoError(t, err)
	assert.Equal(t, uint64(len(data)), md.Size)

	// 2. Read Data
	r, _, err := bucket.ReadChunkFile(ctx, objID, 0)
	require.NoError(t, err)

	readBuf, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, data, readBuf)

	err = r.Close()
	require.NoError(t, err)

	// 3. Exist & Remove
	hash := objID.Hash()
	assert.True(t, bucket.Exist(ctx, hash[:]))

	err = bucket.Remove(ctx, objID)
	require.NoError(t, err)

	// Remove sets it as expired (time.Now().Add(-1) or something similar)
	// We need to wait a tiny bit or just verify the behavior
	time.Sleep(1 * time.Second)
	assert.False(t, bucket.Exist(ctx, hash[:]))
	_, err = bucket.Lookup(ctx, objID)
	assert.ErrorIs(t, err, os.ErrNotExist)

	// 4. Discard
	err = bucket.Discard(ctx, objID)
	require.NoError(t, err)

	// Verify completely gone from index
	entry := bucket.idx.Get(objID.Hash())
	assert.Nil(t, entry)
}

func TestRawDiskBucket_ReopenAndRecover(t *testing.T) {
	devPath := createSparseFile(t, 2*1024*1024*1024)

	objID := object.NewID("http://example.com/recover.txt")
	data := []byte("hello world recovery")

	// Phase 1: Write and close (triggers snapshot)
	func() {
		bucket, err := NewBucket("test-bucket", 100, 100, devPath)
		require.NoError(t, err)

		w, _, err := bucket.WriteChunkFile(context.Background(), objID, 0)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
		err = w.Close()
		require.NoError(t, err)
		
		err = bucket.Close() // Explicit close to sync snapshot
		require.NoError(t, err)
	}()

	// Phase 2: Reopen and verify data exists
	func() {
		bucket, err := NewBucket("test-bucket", 100, 100, devPath)
		require.NoError(t, err)
		defer bucket.Close()

		// Verify Index recovered
		md, err := bucket.Lookup(context.Background(), objID)
		if err != nil {
			t.Logf("Lookup failed. Total entries: %d, hash: %x, err: %v", bucket.idx.Count(), objID.Hash(), err)
		}
		require.NoError(t, err)
		assert.Equal(t, uint64(len(data)), md.Size)

		// Verify Cursor recovered
		cursor := bucket.stripe.Cursor()
		assert.Greater(t, cursor, uint64(0))

		// Read Data
		r, _, err := bucket.ReadChunkFile(context.Background(), objID, 0)
		require.NoError(t, err)

		readBuf, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, data, readBuf)
		r.Close()
	}()
}

func TestIndexTable_Concurrent(t *testing.T) {
	idx := NewIndexTable()

	// Concurrent writes
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			id := object.NewID(string(rune(val)))
			idx.Set(id.Hash(), &IndexEntry{
				Offset:    uint64(val * 4096),
				Size:      1024,
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 1000, idx.Count())
}
