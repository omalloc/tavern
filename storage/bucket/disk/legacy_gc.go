package disk

import (
	"context"
	"fmt"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/legacygc"
)

var _ storage.LegacyVaryReclaimer = (*diskBucket)(nil)

func (d *diskBucket) legacyReclaimer() legacygc.Reclaimer {
	return legacygc.Reclaimer{Index: d.indexdb, Journal: func(ctx context.Context, md *object.Metadata) error {
		if err := d.CheckDurability(); err != nil {
			return err
		}
		if err := d.indexdb.(storage.SyncIndexDB).SetSync(ctx, md.ID.Bytes(), md); err != nil {
			return err
		}
		return d.syncIndexDirectories()
	}, Remove: func(ctx context.Context, id *object.ID, index uint32, budget int) (int, bool, error) {
		return legacygc.RemoveDiskChunk(ctx, id.WPathSlice(d.path, index), budget)
	}}
}
func (d *diskBucket) RetireLegacyVaryRoot(ctx context.Context, id *object.ID) error {
	ctx, finish, err := d.handles.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer finish()
	return d.legacyReclaimer().RetireRoot(ctx, id)
}
func (d *diskBucket) ReclaimLegacyVary(ctx context.Context, id *object.ID, budget int) (int, bool, error) {
	ctx, finish, err := d.handles.Begin(ctx, 0)
	if err != nil {
		return 0, false, err
	}
	defer finish()
	processed, done, err := d.legacyReclaimer().Reclaim(ctx, id, budget)
	if done && err == nil {
		d.cache.Remove(id.Hash())
		err = d.sharedkv.Delete(ctx, []byte(fmt.Sprintf("ix/%s/%s", d.ID(), id.Key())))
	}
	return processed, done, err
}
