package memory

import (
	"context"
	"errors"
	"fmt"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/legacygc"
	"os"
)

var _ storage.LegacyVaryReclaimer = (*memoryBucket)(nil)

func (m *memoryBucket) legacyReclaimer() legacygc.Reclaimer {
	return legacygc.Reclaimer{Index: m.indexdb, Journal: func(ctx context.Context, md *object.Metadata) error { return m.indexdb.Set(ctx, md.ID.Bytes(), md) }, Remove: func(ctx context.Context, id *object.ID, index uint32, budget int) (int, bool, error) {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		err := m.fs.Remove(id.WPathSlice(m.path, index))
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		return 1, err == nil, err
	}}
}
func (m *memoryBucket) RetireLegacyVaryRoot(ctx context.Context, id *object.ID) error {
	ctx, finish, err := m.handles.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer finish()
	return m.legacyReclaimer().RetireRoot(ctx, id)
}
func (m *memoryBucket) ReclaimLegacyVary(ctx context.Context, id *object.ID, budget int) (int, bool, error) {
	ctx, finish, err := m.handles.Begin(ctx, 0)
	if err != nil {
		return 0, false, err
	}
	defer finish()
	processed, done, err := m.legacyReclaimer().Reclaim(ctx, id, budget)
	if done && err == nil {
		m.cache.Remove(id.Hash())
		err = m.sharedkv.Delete(ctx, []byte(fmt.Sprintf("ix/%s/%s", m.ID(), id.Key())))
	}
	return processed, done, err
}
