package indexdb

import (
	"context"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/lifecycle"
)

// A factory-created database rejects every late callback after shutdown starts.
// Active operations, including iterator callbacks, finish before backend.Close.
type guardedDB struct {
	storage.IndexDB
	work     lifecycle.Group
	once     sync.Once
	closeErr error
}

var _ storage.SyncIndexDB = (*guardedDB)(nil)
var _ storage.IndexScanner = (*guardedDB)(nil)

func (g *guardedDB) Close() error {
	g.once.Do(func() { g.work.Drain(context.Background()); g.closeErr = g.IndexDB.Close() })
	return g.closeErr
}
func (g *guardedDB) Get(ctx context.Context, key []byte) (*object.Metadata, error) {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer done()
	return g.IndexDB.Get(ctx, key)
}
func (g *guardedDB) Set(ctx context.Context, key []byte, md *object.Metadata) error {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer done()
	return g.IndexDB.Set(ctx, key, md)
}
func (g *guardedDB) Delete(ctx context.Context, key []byte) error {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer done()
	return g.IndexDB.Delete(ctx, key)
}
func (g *guardedDB) Exist(ctx context.Context, key []byte) bool {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return false
	}
	defer done()
	return g.IndexDB.Exist(ctx, key)
}
func (g *guardedDB) Iterate(ctx context.Context, prefix []byte, fn storage.IterateFunc) error {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer done()
	return g.IndexDB.Iterate(ctx, prefix, fn)
}
func (g *guardedDB) Expired(ctx context.Context, fn storage.IterateFunc) error {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer done()
	return g.IndexDB.Expired(ctx, fn)
}
func (g *guardedDB) GC(ctx context.Context) error {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer done()
	return g.IndexDB.GC(ctx)
}
func (g *guardedDB) Persistent() bool {
	db, ok := g.IndexDB.(storage.SyncIndexDB)
	return ok && db.Persistent()
}
func (g *guardedDB) SetSync(ctx context.Context, key []byte, md *object.Metadata) error {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return err
	}
	defer done()
	db, ok := g.IndexDB.(storage.SyncIndexDB)
	if !ok {
		return storage.ErrDurabilityUnsupported
	}
	return db.SetSync(ctx, key, md)
}
func (g *guardedDB) Scan(ctx context.Context, cursor []byte, limit int) ([]*object.Metadata, []byte, error) {
	ctx, done, err := g.work.Begin(ctx, 0)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	db, ok := g.IndexDB.(storage.IndexScanner)
	if !ok {
		return nil, nil, storage.ErrControlUnsupported
	}
	return db.Scan(ctx, cursor, limit)
}
