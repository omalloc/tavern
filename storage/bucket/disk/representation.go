package disk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
// directory entry. Durable coordinators use StoreControlSync instead.
func (d *diskBucket) StoreControl(ctx context.Context, md *object.Metadata) error {
	if !md.ValidControl() {
		return storage.ErrProtectedControl
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.indexdb.Set(ctx, md.ID.Bytes(), md.Clone())
}

var _ storage.DurableControlStore = (*diskBucket)(nil)
var _ storage.DurableRepresentationBucket = (*diskBucket)(nil)

func (d *diskBucket) CheckDurability() error {
	db, ok := d.indexdb.(storage.SyncIndexDB)
	if !ok || !db.Persistent() {
		return storage.ErrDurabilityUnsupported
	}
	return nil
}

func (d *diskBucket) StoreControlSync(ctx context.Context, md *object.Metadata) error {
	if !md.ValidControl() {
		return storage.ErrProtectedControl
	}
	if err := d.CheckDurability(); err != nil {
		return err
	}
	if err := d.indexdb.(storage.SyncIndexDB).SetSync(ctx, md.ID.Bytes(), md.Clone()); err != nil {
		return err
	}
	return d.syncIndexDirectories()
}

func (d *diskBucket) SyncRepresentationData(ctx context.Context, md *object.Metadata) error {
	if err := d.CheckDurability(); err != nil {
		return err
	}
	if md == nil || !md.ID.IsRepresentation() || !md.HasReadableChunks() {
		return storage.ErrProtectedControl
	}
	var failure error
	md.Chunks.Range(func(index uint32) {
		if failure != nil {
			return
		}
		if failure = ctx.Err(); failure != nil {
			return
		}
		file, _, err := d.ReadChunkFile(ctx, md.ID, index)
		if err != nil {
			failure = err
			return
		}
		stat, err := file.Stat()
		want := min(md.BlockSize, md.Size-uint64(index)*md.BlockSize)
		if err == nil && stat.Size() != int64(want) {
			err = fmt.Errorf("representation chunk %d length mismatch", index)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		failure = err
	})
	if failure != nil {
		return failure
	}
	// Chunk paths contain multiple newly-created hash directories. Sync each
	// directory entry through the bucket's parent before publishing metadata.
	if err := syncDirectories(filepath.Dir(md.ID.WPath(d.path)), filepath.Dir(filepath.Clean(d.path))); err != nil {
		return err
	}
	return nil
}

func (d *diskBucket) StoreRepresentationSync(ctx context.Context, md *object.Metadata) error {
	if err := d.CheckDurability(); err != nil {
		return err
	}
	if md == nil || !md.ID.IsRepresentation() || !md.HasReadableChunks() {
		return storage.ErrProtectedControl
	}
	if err := d.indexdb.(storage.SyncIndexDB).SetSync(ctx, md.ID.Bytes(), md.Clone()); err != nil {
		return err
	}
	return d.syncIndexDirectories()
}

func (d *diskBucket) syncIndexDirectories() error {
	return syncDirectories(d.dbPath, filepath.Dir(filepath.Clean(d.dbPath)))
}

func syncDirectories(start, stop string) error {
	start, err := filepath.Abs(start)
	if err != nil {
		return err
	}
	stop, err = filepath.Abs(stop)
	if err != nil {
		return err
	}
	for path := start; ; path = filepath.Dir(path) {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if path == stop {
			return nil
		}
		if filepath.Dir(path) == path {
			return fmt.Errorf("directory sync boundary is not an ancestor")
		}
	}
}
