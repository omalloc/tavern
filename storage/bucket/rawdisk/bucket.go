//go:build linux
// +build linux

package rawdisk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var _ storage.Bucket = (*rawdiskBucket)(nil)

type rawdiskBucket struct {
	id     string
	weight int
	allow  int

	device *Device
	ioMgr  *IOManager
	stripe *StripeManager
	idx    *IndexTable

	mu sync.Mutex
}

// New conforms to the bucketMap factory function signature
func New(opt *storage.BucketConfig, sharedkv storage.SharedKV) (storage.Bucket, error) {
	weight := 100
	// opt.Weight is not defined, use default
	return NewBucket(opt.Path, weight, 100, opt.Path)
}

func NewBucket(id string, weight, allow int, devPath string) (*rawdiskBucket, error) {
	dev, err := OpenDevice(devPath)
	if err != nil {
		return nil, err
	}

	ioMgr, err := NewIOManager(1024) // 1024 queue depth
	if err != nil {
		dev.Close()
		return nil, err
	}

	b := &rawdiskBucket{
		id:     id,
		weight: weight,
		allow:  allow,
		device: dev,
		ioMgr:  ioMgr,
		idx:    NewIndexTable(),
	}

	if err := b.initDevice(); err != nil {
		b.Close()
		return nil, err
	}

	// TODO: Start snapshot goroutine

	return b, nil
}

func (b *rawdiskBucket) initDevice() error {
	// Read Superblock
	sbBlock, err := AlignedBlock(SuperblockSize)
	if err != nil {
		return err
	}
	defer FreeAlignedBlock(sbBlock)

	_, err = b.ioMgr.ReadAt(b.device.file, sbBlock, 0)
	if err != nil && err != io.EOF {
		return err
	}

	sb, err := UnmarshalSuperblock(sbBlock)
	if err != nil {
		// Needs initialization
		sb = &Superblock{
			Magic:          MagicNumber,
			Version:        Version1,
			BlockSize:      4096,
			TotalBlocks:    b.device.Size() / 4096,
			SnapshotOffset: SuperblockSize,
			SnapshotSize:   1024 * 1024 * 1024, // 1GB
			StripeOffset:   SuperblockSize + 1024*1024*1024,
		}
		sb.StripeSize = b.device.Size() - sb.StripeOffset

		data, err := sb.Marshal()
		if err != nil {
			return err
		}
		copy(sbBlock, data)
		_, err = b.ioMgr.WriteAt(b.device.file, sbBlock, 0)
		if err != nil {
			return err
		}
	}

	// Restore Snapshot
	// We should only read what we actually wrote, reading 1GB could be slow or problematic.
	// But since it's sparse and direct IO, we need aligned blocks. Let's read the first 16MB for this prototype.
	snapshotBlock, err := AlignedBlock(16 * 1024 * 1024) 
	if err == nil {
		defer FreeAlignedBlock(snapshotBlock)
		_, err = b.ioMgr.ReadAt(b.device.file, snapshotBlock, int64(sb.SnapshotOffset))
		if err == nil {
			header, err := DeserializeSnapshot(snapshotBlock, b.idx)
			if err != nil {
				// Add simple fmt.Printf or similar for debug
				// Let's just log it or ignore
				fmt.Printf("Snapshot deserialize error: %v\n", err)
			}
			if err == nil && header != nil {
				sb.WriteCursor = header.WriteCursor
			}
		}
	}

	b.stripe = NewStripeManager(sb.StripeOffset, sb.StripeSize, sb.WriteCursor)
	return nil
}

// Bucket interface implementations

func (b *rawdiskBucket) ID() string          { return b.id }
func (b *rawdiskBucket) Weight() int         { return b.weight }
func (b *rawdiskBucket) Allow() int          { return b.allow }
func (b *rawdiskBucket) UseAllow() bool      { return b.allow > 0 }
func (b *rawdiskBucket) Objects() uint64     { return uint64(b.idx.Count()) }
func (b *rawdiskBucket) HasBad() bool        { return false }
func (b *rawdiskBucket) Type() string        { return "rawdisk" }
func (b *rawdiskBucket) StoreType() string   { return "hot" }
func (b *rawdiskBucket) Path() string        { return b.device.file.Name() }
func (b *rawdiskBucket) TopK(k int) []string { return nil }

func (b *rawdiskBucket) Close() error {
	var errs []error
	// Make a final snapshot before closing
	b.mu.Lock()
	if b.stripe != nil {
		data, err := SerializeSnapshot(b.idx, b.stripe.Cursor())
		if err == nil {
			// Write to snapshot area (lazy alloc)
			snapBlock, _ := AlignedBlock(len(data) + 4096)
			if snapBlock != nil {
				copy(snapBlock, data)
				// Pad to 4096
				writeLen := ((len(data) + 4095) / 4096) * 4096
				_, writeErr := b.ioMgr.WriteAt(b.device.file, snapBlock[:writeLen], int64(SuperblockSize))
				if writeErr != nil {
					errs = append(errs, writeErr)
				}
				FreeAlignedBlock(snapBlock)
			}
		}
	}
	b.mu.Unlock()

	if err := b.ioMgr.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := b.device.Close(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Operation interface implementation

func (b *rawdiskBucket) Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	entry := b.idx.Get(id.Hash())
	if entry == nil || entry.IsExpired() {
		return nil, os.ErrNotExist
	}
	// Currently not storing full metadata in Hash Table for memory efficiency.
	// In reality, we might need to read the metadata header from the disk offset.
	return &object.Metadata{ID: id, Size: uint64(entry.Size), ExpiresAt: entry.ExpiresAt}, nil
}

func (b *rawdiskBucket) Touch(ctx context.Context, id *object.ID) {
	// Not implemented for raw disk to minimize writes.
}

func (b *rawdiskBucket) Store(ctx context.Context, meta *object.Metadata) error {
	entry := b.idx.Get(meta.ID.Hash())
	if entry != nil {
		entry.ExpiresAt = meta.ExpiresAt
		entry.Size = uint32(meta.Size)
		b.idx.Set(meta.ID.Hash(), entry)
	}
	return nil
}

func (b *rawdiskBucket) Exist(ctx context.Context, id []byte) bool {
	var h object.IDHash
	copy(h[:], id)
	entry := b.idx.Get(h)
	if entry == nil || entry.IsExpired() {
		return false
	}
	return true
}

func (b *rawdiskBucket) Remove(ctx context.Context, id *object.ID) error {
	entry := b.idx.Get(id.Hash())
	if entry != nil {
		entry.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix() // Set to 1 hour ago
		b.idx.Set(id.Hash(), entry)
	}
	return nil
}

func (b *rawdiskBucket) Discard(ctx context.Context, id *object.ID) error {
	b.idx.Delete(id.Hash())
	return nil
}

func (b *rawdiskBucket) DiscardWithHash(ctx context.Context, hash object.IDHash) error {
	b.idx.Delete(hash)
	return nil
}

func (b *rawdiskBucket) DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error {
	return b.Discard(ctx, id)
}

func (b *rawdiskBucket) DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error {
	return b.Discard(ctx, meta.ID)
}

func (b *rawdiskBucket) Iterate(ctx context.Context, fn func(*object.Metadata) error) error {
	var iterErr error
	b.idx.Range(func(hash object.IDHash, entry *IndexEntry) bool {
		id := object.NewID(string(hash[:])) // Note: Need a reverse lookup in production
		md := &object.Metadata{ID: id, Size: uint64(entry.Size), ExpiresAt: entry.ExpiresAt}
		if err := fn(md); err != nil {
			iterErr = err
			return false
		}
		return true
	})
	return iterErr
}

func (b *rawdiskBucket) Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool {
	if md.ExpiresAt == 0 {
		return false
	}
	return time.Now().Unix() > md.ExpiresAt
}

func (b *rawdiskBucket) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	// For raw disk, we buffer the entire chunk in memory (if small),
	// or stream to a temporary memory buffer, then write to the stripe.
	// Since write requires knowing size or blocking allocations, we use a simple aligned buffered writer.
	return newAlignedWriter(b, id.Hash()), "", nil
}

func (b *rawdiskBucket) ReadChunkFile(ctx context.Context, id *object.ID, index uint32) (storage.File, string, error) {
	entry := b.idx.Get(id.Hash())
	if entry == nil {
		return nil, "", os.ErrNotExist
	}
	// Return a fake File that reads from device
	return newAlignedReader(b, entry.Offset, entry.Size), "", nil
}

func (b *rawdiskBucket) Migrate(ctx context.Context, id *object.ID, dest storage.Bucket) error {
	return errors.New("not implemented")
}

func (b *rawdiskBucket) SetMigration(m storage.Migration) error {
	return nil
}

// ----------------------------------------------------------------------------
// Simple Writer and Reader Implementations
// ----------------------------------------------------------------------------

type alignedWriter struct {
	b    *rawdiskBucket
	hash object.IDHash
	buf  []byte
	size int
}

func newAlignedWriter(b *rawdiskBucket, hash object.IDHash) *alignedWriter {
	buf, _ := AlignedBlock(4096 * 256) // 1MB max cache per chunk for demo
	return &alignedWriter{
		b:    b,
		hash: hash,
		buf:  buf,
	}
}

func (w *alignedWriter) Write(p []byte) (int, error) {
	if w.size+len(p) > len(w.buf) {
		return 0, errors.New("chunk too large for rawdisk demo")
	}
	copy(w.buf[w.size:], p)
	w.size += len(p)
	return len(p), nil
}

func (w *alignedWriter) Close() error {
	defer FreeAlignedBlock(w.buf)
	if w.size == 0 {
		return nil
	}

	// Align to 4096 block
	writeLen := ((w.size + 4095) / 4096) * 4096
	offset, _, err := w.b.stripe.Allocate(uint32(writeLen))
	if err != nil {
		return err
	}

	_, err = w.b.ioMgr.WriteAt(w.b.device.file, w.buf[:writeLen], int64(offset))
	if err != nil {
		return err
	}

	// Update Index Table immediately
	w.b.idx.Set(w.hash, &IndexEntry{
		Offset:    offset,
		Size:      uint32(w.size),
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	})

	return nil
}

type alignedReader struct {
	b      *rawdiskBucket
	offset uint64
	size   uint32
	pos    uint32
}

func newAlignedReader(b *rawdiskBucket, offset uint64, size uint32) *alignedReader {
	return &alignedReader{
		b:      b,
		offset: offset,
		size:   size,
	}
}

func (r *alignedReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}

	readLen := len(p)
	if r.pos+uint32(readLen) > r.size {
		readLen = int(r.size - r.pos)
	}

	// Read requires 4KB alignment, so we allocate, read, and copy
	alignLen := ((readLen + 4095) / 4096) * 4096
	buf, err := AlignedBlock(alignLen)
	if err != nil {
		return 0, err
	}
	defer FreeAlignedBlock(buf)

	_, err = r.b.ioMgr.ReadAt(r.b.device.file, buf, int64(r.offset+uint64(r.pos)))
	if err != nil && err != io.EOF {
		return 0, err
	}

	copy(p, buf[:readLen])
	r.pos += uint32(readLen)

	return readLen, nil
}

func (r *alignedReader) ReadAt(p []byte, off int64) (n int, err error) {
	return 0, errors.New("not implemented")
}

func (r *alignedReader) Write(p []byte) (n int, err error)              { return 0, os.ErrPermission }
func (r *alignedReader) WriteAt(p []byte, off int64) (n int, err error) { return 0, os.ErrPermission }
func (r *alignedReader) Stat() (os.FileInfo, error)                     { return nil, os.ErrNotExist }
func (r *alignedReader) Sync() error                                    { return nil }
func (r *alignedReader) Fd() uintptr                                    { return ^uintptr(0) }
func (r *alignedReader) Name() string                                   { return "rawdisk-file" }
func (r *alignedReader) Close() error                                   { return nil }
