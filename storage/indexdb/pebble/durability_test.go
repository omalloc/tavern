package pebble

import (
	"context"
	"errors"
	"testing"

	pdb "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/storage/indexdb"
)

func TestSetSyncSurvivesUnsyncedDataLossWithOrdinaryNoSync(t *testing.T) {
	fs := vfs.NewCrashableMem()
	raw, err := pdb.Open("db", &pdb.Options{FS: fs})
	if err != nil {
		t.Fatal(err)
	}
	// The crashable filesystem simulates a persistent device, with explicit
	// loss of every unsynced byte. Ordinary writes are deliberately NoSync.
	p := &PebbleDB{db: raw, codec: encoding.GetDefaultCodec(), writeMode: pdb.NoSync, persistent: true}
	t.Cleanup(func() { p.Close() })
	md := &object.Metadata{ID: object.BarrierCatalogID(), RecordKind: object.RecordBarriers, Barriers: &object.BarrierCatalog{
		SchemaVersion: object.RepresentationSchemaVersion, Incarnation: "history", Revision: 2,
		Entries: []object.PrefixBarrier{{CanonicalPrefix: "https://example.com/img/", HardEpoch: 2}},
	}}
	if err := p.SetSync(t.Context(), md.ID.Bytes(), md); err != nil {
		t.Fatal(err)
	}
	crashed := fs.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 0})
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := pdb.Open("db", &pdb.Options{FS: crashed})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovery := &PebbleDB{db: reopened, codec: encoding.GetDefaultCodec()}
	loaded, err := recovery.Get(t.Context(), md.ID.Bytes())
	if err != nil || loaded.Barriers.Revision != 2 || loaded.Barriers.Incarnation != "history" {
		t.Fatal("sync acknowledgement lost under simulated crash", loaded, err)
	}
}

func TestSetSyncRejectsMemoryAndCancelledWrites(t *testing.T) {
	db, err := New(storage.TypeInMemory, indexdb.NewOption(storage.TypeInMemory))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := db.(*PebbleDB)
	if p.Persistent() {
		t.Fatal("memory filesystem claims persistence")
	}
	md := &object.Metadata{ID: object.NewID("https://example.com/a")}
	if err := p.SetSync(t.Context(), md.ID.Bytes(), md); !errors.Is(err, storage.ErrDurabilityUnsupported) {
		t.Fatal(err)
	}
	p.persistent = true // Exercise cancellation without changing the backend.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.SetSync(ctx, md.ID.Bytes(), md); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := p.Get(t.Context(), md.ID.Bytes()); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("cancelled write reached DB", err)
	}
}
