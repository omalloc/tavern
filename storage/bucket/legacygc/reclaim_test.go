package legacygc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type testIndex struct {
	storage.IndexDB
	records       map[string]*object.Metadata
	deleteFailure bool
}

func (i *testIndex) Get(ctx context.Context, key []byte) (*object.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	md := i.records[string(key)]
	if md == nil {
		return nil, storage.ErrKeyNotFound
	}
	return md.Clone(), nil
}
func (i *testIndex) Set(ctx context.Context, key []byte, md *object.Metadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.records[string(key)] = md.Clone()
	return nil
}
func (i *testIndex) Delete(ctx context.Context, key []byte) error {
	if i.deleteFailure {
		return syscall.ENOSPC
	}
	delete(i.records, string(key))
	return nil
}
func TestReclaimJournalsReferencesAndBoundedProgress(t *testing.T) {
	id := object.NewID("https://example.com/a")
	other := object.NewVirtualID(id.Path(), "unrelated")
	md := &object.Metadata{ID: id, Flags: object.FlagVaryIndex, VirtualKey: []string{other.Ext()}}
	for n := uint32(0); n < 4; n++ {
		md.Chunks.Set(n)
	}
	md.Parts.Set(4)
	index := &testIndex{records: map[string]*object.Metadata{string(id.Bytes()): md, string(other.Bytes()): {ID: other}}}
	files := map[uint32]bool{0: true, 1: true, 2: true, 3: true, 4: true}
	reclaimer := Reclaimer{Index: index, Journal: func(ctx context.Context, copy *object.Metadata) error { return index.Set(ctx, copy.ID.Bytes(), copy) }, Remove: func(ctx context.Context, id *object.ID, part uint32, budget int) (int, bool, error) {
		current, _ := index.Get(ctx, id.Bytes())
		if len(current.VirtualKey) != 0 || current.Flags&object.FlagVaryCache == 0 {
			t.Fatal("bytes removed before durable reference retirement")
		}
		delete(files, part)
		return 1, true, nil
	}}
	n, done, err := reclaimer.Reclaim(t.Context(), id, 2)
	if err != nil || done || n != 1 {
		t.Fatal(n, done, err)
	}
	journal, _ := index.Get(t.Context(), id.Bytes())
	if journal.Chunks.Contains(0) || len(journal.VirtualKey) != 0 || len(files) != 4 {
		t.Fatal("partial progress not journaled")
	}
	// A new helper after a restart uses only the persisted journal, not RAM state.
	restarted := reclaimer
	for step := 0; step < 10 && !done; step++ {
		n, done, err = restarted.Reclaim(t.Context(), id, 2)
		if err != nil || n > 2 {
			t.Fatal(n, done, err)
		}
	}
	if !done || len(files) != 0 {
		t.Fatal("bounded cleanup did not finish")
	}
	if _, err = index.Get(t.Context(), other.Bytes()); err != nil {
		t.Fatal("followed VirtualKey into unrelated data")
	}
}
func TestReclaimDiskFullAndDeleteFailureRemainRetryable(t *testing.T) {
	id := object.NewID("https://example.com/a")
	md := &object.Metadata{ID: id, Flags: object.FlagVaryCache}
	md.Chunks.Set(0)
	md.Chunks.Set(1)
	index := &testIndex{records: map[string]*object.Metadata{string(id.Bytes()): md}}
	removals := 0
	failJournal := true
	reclaimer := Reclaimer{Index: index, Journal: func(ctx context.Context, copy *object.Metadata) error {
		if failJournal {
			return syscall.ENOSPC
		}
		return index.Set(ctx, copy.ID.Bytes(), copy)
	}, Remove: func(ctx context.Context, id *object.ID, part uint32, budget int) (int, bool, error) {
		removals++
		return 1, true, nil
	}}
	if _, _, err := reclaimer.Reclaim(t.Context(), id, 8); !errors.Is(err, syscall.ENOSPC) || removals != 0 {
		t.Fatal("journal failure deleted bytes", err, removals)
	}
	failJournal = false
	index.deleteFailure = true
	if _, _, err := reclaimer.Reclaim(t.Context(), id, 8); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	journal, err := index.Get(t.Context(), id.Bytes())
	if err != nil || journal.Chunks.Count() != 0 {
		t.Fatal("delete failure lost progress", err)
	}
	index.deleteFailure = false
	if n, done, err := reclaimer.Reclaim(t.Context(), id, 2); n != 0 || !done || err != nil {
		t.Fatal("empty journal could not resume", n, done, err)
	}
}
func TestDiskCleanupBoundsTemporaryFilesAndRetriesFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owned-000000")
	for _, name := range []string{path, path + ".tmp1", path + ".tmp2", path + ".tmp3", filepath.Join(dir, "unrelated.tmp1")} {
		if err := os.WriteFile(name, []byte("bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	done := false
	for pass := 0; pass < 10 && !done; pass++ {
		n, complete, err := RemoveDiskChunk(t.Context(), path, 2)
		if err != nil || n > 2 {
			t.Fatal(n, err)
		}
		done = complete
	}
	if !done {
		t.Fatal("temp cleanup did not finish")
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated.tmp1")); err != nil {
		t.Fatal("removed another identity")
	}
	// A directory substituted for a file is an actual deletion failure, not an
	// acknowledged success. After repair the same owned path can be retried.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0600)
	if _, _, err := RemoveDiskChunk(t.Context(), path, 2); err == nil {
		t.Fatal("swallowed physical deletion failure")
	}
	os.RemoveAll(path)
	if _, done, err := RemoveDiskChunk(t.Context(), path, 2); err != nil || !done {
		t.Fatal(done, err)
	}
}
func TestReclaimRejectsNormalUnknownAndReservedRecords(t *testing.T) {
	key, _ := object.NewResourceKey("https://example.com/a", "v2", "")
	reserved, _ := key.RepresentationID("inc", 0, fmt.Sprintf("%064x", 1), "gen")
	for _, md := range []*object.Metadata{{ID: object.NewID(key.CanonicalURL())}, {ID: object.NewID(key.CanonicalURL()), Flags: 8, VirtualKey: []string{"x"}}, {ID: reserved, Flags: object.FlagVaryCache}, {ID: object.BarrierCatalogID(), Flags: object.FlagVaryCache}, {ID: object.NewID(key.CanonicalURL()), RecordKind: "future", Flags: object.FlagVaryCache}} {
		if Eligible(md) {
			t.Fatal("unsafe maintenance eligibility", md)
		}
	}
}

func TestReclaimRejectsMismatchedIndexIdentity(t *testing.T) {
	id := object.NewID("https://example.com/a")
	other := object.NewID("https://example.com/b")
	index := &testIndex{records: map[string]*object.Metadata{string(id.Bytes()): {ID: other, Flags: object.FlagVaryCache}}}
	reclaimer := Reclaimer{Index: index, Journal: func(context.Context, *object.Metadata) error {
		t.Fatal("mismatched metadata was journaled")
		return nil
	}, Remove: func(context.Context, *object.ID, uint32, int) (int, bool, error) {
		t.Fatal("mismatched metadata deleted bytes")
		return 0, false, nil
	}}
	if err := reclaimer.RetireRoot(t.Context(), id); !errors.Is(err, ErrNotRetired) {
		t.Fatal("root index identity mismatch was accepted", err)
	}
	if _, _, err := reclaimer.Reclaim(t.Context(), id, 2); !errors.Is(err, ErrNotRetired) {
		t.Fatal("reclaim index identity mismatch was accepted", err)
	}
}
