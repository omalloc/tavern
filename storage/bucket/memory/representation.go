package memory

import (
	"context"
	"errors"
	"fmt"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"os"
)

var _ storage.ControlStore = (*memoryBucket)(nil)
var _ storage.MetadataScanner = (*memoryBucket)(nil)
var _ storage.RepresentationEviction = (*memoryBucket)(nil)
var _ storage.VolatileRepresentationBucket = (*memoryBucket)(nil)

func (m *memoryBucket) VolatileRepresentations() bool { return true }

func (m *memoryBucket) SetRepresentationEvictor(handler func(context.Context, *object.Metadata) error) {
	m.representationMu.Lock()
	defer m.representationMu.Unlock()
	m.representationEvictor = handler
}

func (m *memoryBucket) discardRepresentation(ctx context.Context, md *object.Metadata) error {
	chunks := md.Chunks.Clone(nil)
	md.Parts.Range(func(index uint32) { chunks.Set(index) })
	var failure error
	chunks.Range(func(index uint32) {
		if failure != nil {
			return
		}
		if failure = ctx.Err(); failure != nil {
			return
		}
		if err := m.fs.Remove(md.ID.WPathSlice(m.path, index)); err != nil && !errors.Is(err, os.ErrNotExist) {
			failure = err
		}
	})
	if failure != nil {
		return failure
	}
	if err := m.indexdb.Delete(ctx, md.ID.Bytes()); err != nil {
		return err
	}
	m.cache.Remove(md.ID.Hash())
	return m.sharedkv.Delete(ctx, []byte(fmt.Sprintf("ix/%s/%s", m.ID(), md.ID.Key())))
}

func (m *memoryBucket) ScanMetadata(ctx context.Context, after []byte, limit int) ([]*object.Metadata, []byte, error) {
	scanner, ok := m.indexdb.(storage.IndexScanner)
	if !ok {
		return nil, nil, storage.ErrControlUnsupported
	}
	return scanner.Scan(ctx, after, limit)
}

// LoadControl bypasses ordinary object lookup and its access side effects.
func (m *memoryBucket) LoadControl(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	if !id.IsControl() {
		return nil, storage.ErrKeyNotFound
	}
	md, err := m.indexdb.Get(ctx, id.Bytes())
	if err != nil {
		return nil, err
	}
	if !md.IsControl() {
		return nil, storage.ErrKeyNotFound
	}
	return md, nil
}

// StoreControl does not merge bitmaps, update LRU, or register an ordinary
// directory entry. This memory bucket never claims persistent durability.
func (m *memoryBucket) StoreControl(ctx context.Context, md *object.Metadata) error {
	if !md.ValidControl() {
		return storage.ErrProtectedControl
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.indexdb.Set(ctx, md.ID.Bytes(), md.Clone())
}
