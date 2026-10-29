package disk

import (
	"context"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var _ storage.ControlStore = (*diskBucket)(nil)

// LoadControl bypasses ordinary object lookup and its access side effects.
func (d *diskBucket) LoadControl(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	if !id.IsControl() {
		return nil, storage.ErrKeyNotFound
	}
	md, err := d.indexdb.Get(ctx, id.Bytes())
	if err != nil {
		return nil, err
	}
	if !md.IsControl() {
		return nil, storage.ErrKeyNotFound
	}
	return md, nil
}

// StoreControl does not merge bitmaps, update LRU, or register an ordinary
// directory entry. Persistent invalidation durability is not enabled in P1.3.
func (d *diskBucket) StoreControl(ctx context.Context, md *object.Metadata) error {
	if md == nil || md.RecordKind != object.RecordResource || !md.ID.IsControl() || md.Resource == nil ||
		md.Resource.SchemaVersion != object.RepresentationSchemaVersion || md.Representation != nil ||
		len(md.Headers) != 0 || len(md.VirtualKey) != 0 || md.Code != 0 || md.Size != 0 ||
		md.Chunks.Count() != 0 || md.Parts.Count() != 0 || md.Flags != object.FlagCache {
		return storage.ErrProtectedControl
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.indexdb.Set(ctx, md.ID.Bytes(), md.Clone())
}
