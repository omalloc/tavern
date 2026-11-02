package pebble

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pdb "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/encoding"
)

func writebackDB(t *testing.T) (*PebbleDB, *vfs.MemFS) {
	t.Helper()
	fs := vfs.NewCrashableMem()
	db, err := pdb.Open("db", &pdb.Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	// No timer: explicitly advance batches/crashes without timing assumptions.
	p := &PebbleDB{db: db, codec: encoding.GetDefaultCodec(), writeMode: pdb.NoSync, persistent: true}
	t.Cleanup(func() { p.Close() })
	return p, fs
}

func TestWritebackOverlayCoalescesAndScansPendingMutations(t *testing.T) {
	p, _ := writebackDB(t)
	for i := range 10 {
		id := object.NewID(fmt.Sprintf("https://example.com/%d", i))
		for revision := range 3 {
			if err := p.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id, Size: uint64(revision)}); err != nil {
				t.Fatal(err)
			}
		}
		md, err := p.Get(t.Context(), id.Bytes())
		if err != nil || md.Size != 2 {
			t.Fatal("pending update was not immediately visible", md, err)
		}
		if _, closer, err := p.db.Get(id.Bytes()); !errors.Is(err, pdb.ErrNotFound) {
			if closer != nil {
				closer.Close()
			}
			t.Fatal("ordinary Set bypassed batch queue", err)
		}
	}
	if len(p.writeback.pending) != 10 {
		t.Fatal("repeated updates were not coalesced", len(p.writeback.pending))
	}
	var cursor []byte
	var seen int
	for {
		rows, next, err := p.Scan(t.Context(), cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		seen += len(rows)
		if next == nil {
			break
		}
		cursor = next
	}
	if seen != 10 {
		t.Fatal("bounded scan omitted pending metadata", seen)
	}
	// Iterator callbacks may mutate the database without deadlocking, and a
	// pending deletion must hide a committed record in both Get and Scan.
	if err := p.flushWriteback(); err != nil {
		t.Fatal(err)
	}
	if err := p.Iterate(t.Context(), nil, func(key []byte, md *object.Metadata) bool {
		if err := p.Delete(t.Context(), key); err != nil {
			t.Error(err)
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := p.Scan(t.Context(), nil, 20)
	if err != nil || len(rows) != 0 {
		t.Fatal("pending deletions remained visible", rows, err)
	}
}

func TestWritebackSyncBarrierDrainsOlderUpdatesBeforeCrash(t *testing.T) {
	p, fs := writebackDB(t)
	id := object.NewID("https://example.com/cache-root")
	if err := p.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id, Size: 1}); err != nil {
		t.Fatal(err)
	}
	barrier := &object.Metadata{ID: object.BarrierCatalogID(), RecordKind: object.RecordBarriers,
		Barriers: &object.BarrierCatalog{SchemaVersion: object.RepresentationSchemaVersion, Incarnation: "history", Revision: 7}}
	if err := p.SetSync(t.Context(), barrier.ID.Bytes(), barrier); err != nil {
		t.Fatal(err)
	}
	// A later cache write can be lost; the acknowledged barrier cannot.
	if err := p.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id, Size: 2}); err != nil {
		t.Fatal(err)
	}
	crash := fs.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 0})
	reopened, err := pdb.Open("db", &pdb.Options{FS: crash})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovery := &PebbleDB{db: reopened, codec: p.codec}
	md, err := recovery.Get(t.Context(), barrier.ID.Bytes())
	if err != nil || md.Barriers.Revision != 7 {
		t.Fatal("acknowledged barrier lost after power failure", md, err)
	}
	md, err = recovery.Get(t.Context(), id.Bytes())
	if err != nil || md.Size != 1 {
		t.Fatal("older queued update was not ordered before the barrier", md, err)
	}
	// Same-key invalidation must also supersede pending older state forever.
	if err := p.SetSync(t.Context(), id.Bytes(), &object.Metadata{ID: id, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if err := p.flushWriteback(); err != nil {
		t.Fatal(err)
	}
	md, err = p.Get(t.Context(), id.Bytes())
	if err != nil || md.Size != 3 || len(p.writeback.pending) != 0 {
		t.Fatal("background batch replayed state older than invalidation", md, err)
	}
}

func TestWritebackQueueIsBoundedAndCloseDrains(t *testing.T) {
	p, _ := writebackDB(t)
	for i := range writebackMaxKeys + 1 {
		id := object.NewID(fmt.Sprint(i))
		if err := p.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.writeback.pending) != 1 {
		t.Fatal("key budget did not flush a bounded batch", len(p.writeback.pending))
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if len(p.writeback.pending) != 0 {
		t.Fatal("shutdown left accepted writes pending")
	}
}

func TestWritebackV2DoesNotInheritLegacySyncSetting(t *testing.T) {
	p, _ := writebackDB(t)
	p.writeMode = pdb.Sync
	key, _ := object.NewResourceKey("https://example.com/a", "http-v2", "origin")
	md := &object.Metadata{ID: key.ID(), RecordKind: object.RecordResource}
	if err := p.Set(t.Context(), md.ID.Bytes(), md); err != nil {
		t.Fatal(err)
	}
	if _, closer, err := p.db.Get(md.ID.Bytes()); !errors.Is(err, pdb.ErrNotFound) {
		if closer != nil {
			closer.Close()
		}
		t.Fatal("cache root inherited per-write Sync", err)
	}
	if err := p.SetSync(t.Context(), md.ID.Bytes(), md); err != nil {
		t.Fatal(err)
	}
	if len(p.writeback.pending) != 0 {
		t.Fatal("explicit barrier did not drain the cache queue")
	}
}

type blockedWALFS struct {
	vfs.FS
	block            atomic.Bool
	entered, release chan struct{}
	once             sync.Once
}

func (fs *blockedWALFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	file, err := fs.FS.Create(name, category)
	if err == nil && strings.HasSuffix(name, ".log") {
		return &blockedWALFile{File: file, owner: fs}, nil
	}
	return file, err
}

type blockedWALFile struct {
	vfs.File
	owner *blockedWALFS
}

func (f *blockedWALFile) wait() {
	if f.owner.block.Load() {
		f.owner.once.Do(func() { close(f.owner.entered) })
		<-f.owner.release
	}
}
func (f *blockedWALFile) Sync() error     { f.wait(); return f.File.Sync() }
func (f *blockedWALFile) SyncData() error { f.wait(); return f.File.SyncData() }

func TestWritebackBackgroundSyncDoesNotBlockOrdinaryWrites(t *testing.T) {
	fs := &blockedWALFS{FS: vfs.NewMem(), entered: make(chan struct{}), release: make(chan struct{})}
	db, err := pdb.Open("db", &pdb.Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	p := &PebbleDB{db: db, codec: encoding.GetDefaultCodec(), writeMode: pdb.NoSync}
	defer p.Close()
	var release sync.Once
	defer release.Do(func() { close(fs.release) })
	fs.block.Store(true)
	p.startWriteback()
	id := object.NewID("https://example.com/async")
	if err := p.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id, Size: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background timer did not flush the pending batch")
	}
	written := make(chan error, 1)
	go func() {
		written <- p.Set(t.Context(), id.Bytes(), &object.Metadata{ID: id, Size: 2})
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary write waited for background WAL sync")
	}
	md, err := p.Get(t.Context(), id.Bytes())
	if err != nil || md.Size != 2 {
		t.Fatal("new pending value hidden during background sync", md, err)
	}
	release.Do(func() { close(fs.release) })
}
