package representation_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/memory"
	"github.com/omalloc/tavern/storage/representation"
	"github.com/omalloc/tavern/storage/sharedkv"
)

type managedBucket interface {
	storage.DurableRepresentationBucket
	storage.DurableControlStore
	storage.MetadataScanner
	storage.RepresentationEviction
}

type migrationProbe struct {
	unhealthy atomic.Bool
	managedBucket
	fail             atomic.Value // string
	entered, release chan struct{}
	once             sync.Once
}

func (b *migrationProbe) UseAllow() bool { return !b.unhealthy.Load() }

func (b *migrationProbe) failure(stage string) error {
	if value := b.fail.Load(); value != nil && value.(string) == stage {
		return fmt.Errorf("injected %s failure", stage)
	}
	return nil
}
func (b *migrationProbe) WriteChunkFile(ctx context.Context, id *object.ID, index uint32) (io.WriteCloser, string, error) {
	if b.entered != nil {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if err := b.failure("write"); err != nil {
		return nil, "", err
	}
	w, path, err := b.managedBucket.WriteChunkFile(ctx, id, index)
	if err == nil {
		w = &migrationWriter{WriteCloser: w, bucket: b}
	}
	return w, path, err
}

type migrationWriter struct {
	io.WriteCloser
	bucket *migrationProbe
}

func (w *migrationWriter) Close() error {
	err := w.WriteCloser.Close()
	if err != nil {
		return err
	}
	return w.bucket.failure("close")
}
func (b *migrationProbe) SyncRepresentationData(ctx context.Context, md *object.Metadata) error {
	if err := b.failure("data"); err != nil {
		return err
	}
	return b.managedBucket.SyncRepresentationData(ctx, md)
}
func (b *migrationProbe) StoreRepresentationSync(ctx context.Context, md *object.Metadata) error {
	if err := b.failure("child"); err != nil {
		return err
	}
	return b.managedBucket.StoreRepresentationSync(ctx, md)
}
func (b *migrationProbe) StoreControlSync(ctx context.Context, md *object.Metadata) error {
	if md.Resource != nil {
		if err := b.failure("root"); err != nil {
			return err
		}
	}
	return b.managedBucket.StoreControlSync(ctx, md)
}

type managerFixture struct {
	m        *representation.Manager
	repo     *representation.IndexRepository
	key      object.ResourceKey
	buckets  []*migrationProbe
	stateDir string
}

func newManagerFixture(t *testing.T) *managerFixture {
	t.Helper()
	buckets := []*migrationProbe{{managedBucket: durableBucket(t, "", "pebble").(managedBucket)}, {managedBucket: durableBucket(t, "", "pebble").(managedBucket)}}
	f := &managerFixture{buckets: buckets, stateDir: t.TempDir()}
	f.key, _ = object.NewResourceKey("https://example.com/img/item?a=1&a=2", "http-v2", "isolated")
	f.open(t)
	return f
}
func (f *managerFixture) open(t *testing.T) {
	t.Helper()
	m, err := representation.NewManager(t.Context(), []storage.Bucket{f.buckets[1], f.buckets[0]}, representation.ManagerConfig{StateDirectory: f.stateDir, Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	t.Cleanup(func() { m.Close() })
	f.repo, err = m.Repository(f.key)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *managerFixture) bucket(location string) *migrationProbe {
	for _, b := range f.buckets {
		if b.ID() == location {
			return b
		}
	}
	panic("unknown location")
}
func (f *managerFixture) locations(t *testing.T) (*migrationProbe, *migrationProbe) {
	t.Helper()
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	source := f.bucket(state.Resource().Resource.ActiveLocation)
	for _, b := range f.buckets {
		if b != source {
			return source, b
		}
	}
	panic("missing destination")
}

// Begin, bind and persist the journal before writing any bytes, just as the
// streaming admission path does. No test-only Update can publish a reference.
func (f *managerFixture) candidate(t *testing.T, ref object.VariantRef, generation, body string, partial bool) (*representation.IndexState, *representation.Reservation, object.VariantRef, *object.Metadata) {
	t.Helper()
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	root := state.Resource().Resource
	id, err := f.key.RepresentationID(root.Incarnation, root.PurgeEpoch, ref.SelectorFingerprint, generation)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	ref.RepresentationID, ref.ResponseDate = id, now
	md := &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, Size: uint64(len(body)), BlockSize: 4, Code: 200, RespUnix: now, ExpiresAt: now + 3600,
		Headers:        http.Header{"Cache-Control": {"public, max-age=3600"}, "Content-Length": {fmt.Sprint(len(body))}, "Vary": {strings.Join(ref.VaryFields, ", ")}},
		Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion, ResourceID: f.key.ResourceID(), Incarnation: root.Incarnation, PurgeEpoch: root.PurgeEpoch, ValidatedEpoch: root.RevalidateEpoch, BodyGeneration: generation, StorageSelectorFingerprint: ref.SelectorFingerprint, Kind: object.RepresentationOrigin, State: object.RepresentationComplete}}
	if partial {
		md.Representation.State = object.RepresentationFilling
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.BindFill(t.Context(), state, lease, md); err != nil {
		t.Fatal(err)
	}
	bucket := f.bucket(root.ActiveLocation)
	for offset := 0; offset < len(body); offset += 4 {
		index := uint32(offset / 4)
		if partial && index == 1 {
			continue
		}
		md.Parts.Set(index)
		if err = f.repo.JournalFill(t.Context(), state, lease, bucket, md); err != nil {
			t.Fatal(err)
		}
		w, _, err := bucket.WriteChunkFile(t.Context(), id, index)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(w, body[offset:min(offset+4, len(body))]); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		md.Parts.Remove(index)
		md.Chunks.Set(index)
	}
	return state, lease, ref, md
}
func (f *managerFixture) publish(t *testing.T, ref object.VariantRef, generation, body string, partial bool) (object.VariantRef, *object.Metadata) {
	t.Helper()
	state, lease, ref, md := f.candidate(t, ref, generation, body, partial)
	f.commit(t, state, lease, ref, md)
	ref.Revision = md.Representation.Revision
	return ref, md
}
func (f *managerFixture) commit(t *testing.T, state *representation.IndexState, lease *representation.Reservation, ref object.VariantRef, md *object.Metadata) {
	t.Helper()
	bucket := f.bucket(state.Resource().Resource.ActiveLocation)
	ok, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return bucket.Store(t.Context(), md) })
	if err != nil || !ok {
		t.Fatal("publish", ok, err)
	}
}
func httpSelector(t *testing.T, value string) object.VariantRef {
	t.Helper()
	ref, err := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {value}})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func (f *managerFixture) collect(t *testing.T) {
	t.Helper()
	for range 8 {
		result, err := f.m.CollectUnreachable(t.Context(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if result.Scanned > 2 {
			t.Fatal("unbounded scan", result)
		}
	}
}
func readChunk(t *testing.T, b storage.Bucket, id *object.ID, index uint32) string {
	t.Helper()
	r, _, err := b.ReadChunkFile(t.Context(), id, index)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func absent(t *testing.T, b storage.Bucket, id *object.ID) {
	t.Helper()
	if _, err := b.Lookup(t.Context(), id); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("metadata retained", err)
	}
	r, _, err := b.ReadChunkFile(t.Context(), id, 0)
	if r != nil {
		r.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bytes retained", err)
	}
}

func TestManagerCollectionMigrationAndReaderPins(t *testing.T) {
	f := newManagerFixture(t)
	source, target := f.locations(t)
	_, original := f.publish(t, httpSelector(t, "jpeg"), "original", "original-image", false)
	partialRef, partial := f.publish(t, httpSelector(t, "partial"), "partial", "abcdefghij", true)
	selected, err := f.repo.LookupRead(t.Context(), f.key, http.Header{"Accept": {"jpeg"}})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Lease.Close()
	derivedRef, err := representation.NewManagedSelector(selected, object.TransformFingerprint([]byte(`{"quality":80}`), "p1", "e1"))
	if err != nil {
		t.Fatal(err)
	}
	state, lease, derivedRef, derived := f.candidate(t, derivedRef, "derived", "webp", false)
	derived.Representation.Kind = object.RepresentationDerived
	derived.Representation.SourceID = original.ID.Clone()
	derived.Representation.SourceVersion = "original"
	derived.Representation.TransformSpec = []byte(`{"quality":80}`)
	derived.Representation.PolicyVersion = "p1"
	derived.Representation.EncoderVersion = "e1"
	derived.ExpiresAt = original.ExpiresAt
	f.commit(t, state, lease, derivedRef, derived)
	duplicate := selected.Lease.Duplicate()
	if duplicate == nil {
		t.Fatal("duplicate reader missing")
	}
	before, _ := f.repo.Load(t.Context(), f.key)
	if err = f.repo.MigrateResource(t.Context(), f.key, target.ID()); err != nil {
		t.Fatal(err)
	}
	after, _ := f.repo.Load(t.Context(), f.key)
	if after.Resource.ActiveLocation != target.ID() || after.Resource.RoutingEpoch != before.Resource.RoutingEpoch+1 || len(after.Resource.Variants) != 3 {
		t.Fatal(after.Resource)
	}
	if err = f.repo.MigrateResource(t.Context(), f.key, source.ID()); !errors.Is(err, representation.ErrMigrationBusy) {
		t.Fatal("overwrote pinned old location", err)
	}
	if got := readChunk(t, target, original.ID, 0); got != "orig" {
		t.Fatal(got)
	}
	if got := readChunk(t, target, derived.ID, 0); got != "webp" {
		t.Fatal(got)
	}
	moved, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"partial"}})
	if err != nil || moved.Representation.Chunks.Count() != 2 || moved.Representation.HasComplete() || moved.Reference.SelectorFingerprint != partialRef.SelectorFingerprint {
		t.Fatal(moved, err)
	}
	if got := readChunk(t, target, partial.ID, 2); got != "ij" {
		t.Fatal(got)
	}
	f.collect(t)
	if got := readChunk(t, source, original.ID, 0); got != "orig" {
		t.Fatal("pinned source lost", got)
	}
	selected.Lease.Close()
	if selected.Lease.Duplicate() != nil {
		t.Fatal("closed lease cloned")
	}
	f.collect(t)
	if got := readChunk(t, source, original.ID, 0); got != "orig" {
		t.Fatal("duplicate pin lost", got)
	}
	duplicate.Close()
	f.collect(t)
	absent(t, source, original.ID)
	absent(t, source, derived.ID)
	absent(t, source, partial.ID)
	if _, err = f.repo.LookupDerived(t.Context(), f.key, original.ID, "original", derivedRef.TransformFingerprint); err != nil {
		t.Fatal("migrated dependency missing", err)
	}
	f.m.Close()
	f.open(t)
	recovered, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"jpeg"}})
	if err != nil || recovered.Resource.Resource.ActiveLocation != target.ID() {
		t.Fatal(recovered, err)
	}
}

func TestManagerMigrationFailuresLeaveSourceAndCollectTarget(t *testing.T) {
	for _, failure := range []string{"write", "close", "data", "child", "root"} {
		t.Run(failure, func(t *testing.T) {
			f := newManagerFixture(t)
			source, target := f.locations(t)
			_, md := f.publish(t, httpSelector(t, "jpeg"), "original", "original-image", false)
			if failure == "root" {
				source.fail.Store(failure)
			} else {
				target.fail.Store(failure)
			}
			if err := f.repo.MigrateResource(t.Context(), f.key, target.ID()); err == nil {
				t.Fatal("failure ignored")
			}
			source.fail.Store("")
			target.fail.Store("")
			result, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"jpeg"}})
			if err != nil || result.Resource.Resource.ActiveLocation != source.ID() {
				t.Fatal(result, err)
			}
			if readChunk(t, source, md.ID, 0) != "orig" {
				t.Fatal("source corrupted")
			}
			f.collect(t)
			absent(t, target, md.ID)
			// A failed migration releases its marker and pins, so a retry can commit.
			if err = f.repo.MigrateResource(t.Context(), f.key, target.ID()); err != nil {
				t.Fatal("retry blocked", err)
			}
		})
	}
}

func TestManagerMigrationPurgeAndLateFill(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprint(hard), func(t *testing.T) {
			f := newManagerFixture(t)
			source, target := f.locations(t)
			_, original := f.publish(t, httpSelector(t, "jpeg"), "original", "original-image", false)
			state, lease, ref, late := f.candidate(t, httpSelector(t, "png"), "late", "png-body", false)
			target.entered, target.release = make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- f.repo.MigrateResource(t.Context(), f.key, target.ID()) }()
			select {
			case <-target.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("migration did not start")
			}
			if _, err := f.repo.Capture(t.Context(), f.key); !errors.Is(err, representation.ErrMigrationBusy) {
				t.Fatal("fill accepted during migration", err)
			}
			// The copier holds no barrier/resource lock during file I/O.
			mode := representation.InvalidateSoft
			if hard {
				mode = representation.InvalidateHard
			}
			if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/img/", mode); err != nil {
				t.Fatal(err)
			}
			close(target.release)
			if err := <-done; !errors.Is(err, representation.ErrSuperseded) {
				t.Fatal("purge lost to migration", err)
			}
			ok, err := f.repo.Publish(t.Context(), state, lease, ref, late, nil, func() error { return source.Store(t.Context(), late) })
			if err != nil || ok {
				t.Fatal("late fill resurrected", ok, err)
			}
			result, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"jpeg"}})
			if hard {
				if !errors.Is(err, storage.ErrKeyNotFound) {
					t.Fatal(result, err)
				}
			} else if err != nil || result.Fresh {
				t.Fatal("soft purge became fresh", result, err)
			}
			f.collect(t)
			absent(t, target, original.ID)
		})
	}
}

func TestManagerStableTopologyAndNoFallback(t *testing.T) {
	f := newManagerFixture(t)
	home, err := f.m.HomeID(f.key.ResourceID())
	if err != nil {
		t.Fatal(err)
	}
	f.m.Close()
	f.open(t)
	again, _ := f.m.HomeID(f.key.ResourceID())
	if home != again {
		t.Fatal("owner changed with config order")
	}
	for _, b := range f.buckets {
		b.unhealthy.Store(true)
	}
	selected, err := f.m.Repository(f.key)
	if err != nil || selected != f.repo {
		t.Fatal("owner followed health", err)
	}
	f.m.Close()
	if _, err = representation.NewManager(t.Context(), []storage.Bucket{f.buckets[0]}, representation.ManagerConfig{StateDirectory: f.stateDir, Durable: true}); !errors.Is(err, representation.ErrTopologyMismatch) {
		t.Fatal("topology silently shrank", err)
	}
	other := durableBucket(t, "", "pebble")
	if _, err = representation.NewManager(t.Context(), []storage.Bucket{other}, representation.ManagerConfig{StateDirectory: f.stateDir, Durable: true}); !errors.Is(err, representation.ErrTopologyMismatch) {
		t.Fatal("entire topology silently replaced", err)
	}
	data, err := os.ReadFile(filepath.Join(f.stateDir, "representation-topology.json"))
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
}

func TestManagerRecoveryAndBoundedOrphanCollection(t *testing.T) {
	f := newManagerFixture(t)
	source, _ := f.locations(t)
	_, md := f.publish(t, httpSelector(t, "jpeg"), "original", "original-image", false)
	state, lease, _, orphan := f.candidate(t, httpSelector(t, "png"), "orphan", "png-body", false)
	_ = state
	// Active reservations protect journaled bytes even before publication.
	f.collect(t)
	if readChunk(t, source, orphan.ID, 0) != "png-" {
		t.Fatal("active fill collected")
	}
	f.repo.Release(lease)
	// Truncate a published chunk; recovery retires it without attaching orphan.
	path := md.ID.WPathSlice(source.Path(), 0)
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.m.Close()
	f.open(t)
	if _, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"jpeg"}}); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("truncated child restored", err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"png"}}); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("orphan reattached", err)
	}
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != 0 {
		t.Fatal(root, err)
	}
	f.collect(t)
	absent(t, source, md.ID)
	absent(t, source, orphan.ID)
	if _, err = f.repo.Load(t.Context(), f.key); err != nil {
		t.Fatal("control state collected", err)
	}
}

func TestManagerMemoryRetirementPreservesPinnedData(t *testing.T) {
	b, err := memory.New(&storage.BucketConfig{Driver: storage.TypeInMemory, Type: storage.TypeHot, MaxObjectLimit: 1}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	m, err := representation.NewManager(t.Context(), []storage.Bucket{b}, representation.ManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	key, _ := object.NewResourceKey("https://example.com/img/a", "v2", "")
	repo, _ := m.Repository(key)
	// Use the same lifecycle fixture against volatile storage via a thin wrapper.
	f := &managerFixture{m: m, repo: repo, key: key, buckets: []*migrationProbe{{managedBucket: &volatileManagedBucket{Bucket: b, ControlStore: b.(storage.ControlStore), MetadataScanner: b.(storage.MetadataScanner), RepresentationEviction: b.(storage.RepresentationEviction)}}}}
	_, md := f.publish(t, httpSelector(t, "jpeg"), "original", "body", false)
	pinned, err := repo.LookupRead(t.Context(), key, http.Header{"Accept": {"jpeg"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.EvictReference(t.Context(), b.ID(), md); err != nil {
		t.Fatal(err)
	}
	root, err := repo.Load(t.Context(), key)
	if err != nil || len(root.Resource.Variants) != 0 {
		t.Fatal("reference not retired", root, err)
	}

	if readChunk(t, b, md.ID, 0) != "body" {
		t.Fatal("evicted bytes deleted under reader")
	}
	pinned.Lease.Close()
	for range 4 {
		if _, err = m.CollectUnreachable(t.Context(), 2); err != nil {
			t.Fatal(err)
		}
	}
	absent(t, b, md.ID)
}

type volatileManagedBucket struct {
	storage.Bucket
	storage.ControlStore
	storage.MetadataScanner
	storage.RepresentationEviction
}

func (*volatileManagedBucket) CheckDurability() error { return storage.ErrDurabilityUnsupported }
func (*volatileManagedBucket) StoreControlSync(context.Context, *object.Metadata) error {
	return storage.ErrDurabilityUnsupported
}
func (*volatileManagedBucket) SyncRepresentationData(context.Context, *object.Metadata) error {
	return storage.ErrDurabilityUnsupported
}
func (*volatileManagedBucket) StoreRepresentationSync(context.Context, *object.Metadata) error {
	return storage.ErrDurabilityUnsupported
}

func (*volatileManagedBucket) StoreRepresentationJournalSync(context.Context, *object.Metadata) error {
	return storage.ErrDurabilityUnsupported
}

func TestManagerSupersededFillKeepsIndependentWriterPin(t *testing.T) {
	f := newManagerFixture(t)
	source, _ := f.locations(t)
	_, lease, ref, md := f.candidate(t, httpSelector(t, "jpeg"), "old-fill", "old-body", false)
	if err := f.repo.Invalidate(t.Context(), f.key, representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal("old budget not released", err)
	}
	f.repo.Release(newer)
	f.collect(t)
	if readChunk(t, source, md.ID, 0) != "old-" {
		t.Fatal("superseded active writer was collected")
	}
	f.repo.Release(lease)
	f.repo.Release(lease)
	f.collect(t)
	absent(t, source, md.ID)
}

func TestManagerEvictionRetiresSourceDependenciesAndRespectsReaders(t *testing.T) {
	f := newManagerFixture(t)
	source, _ := f.locations(t)
	_, original := f.publish(t, httpSelector(t, "jpeg"), "original", "original-image", false)
	selected, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"jpeg"}})
	if err != nil {
		t.Fatal(err)
	}
	spec := []byte(`{"quality":80}`)
	ref, err := representation.NewManagedSelector(selected, object.TransformFingerprint(spec, "p1", "e1"))
	if err != nil {
		t.Fatal(err)
	}
	state, lease, ref, md := f.candidate(t, ref, "derived", "webp", false)
	md.Representation.Kind = object.RepresentationDerived
	md.Representation.SourceID = original.ID.Clone()
	md.Representation.SourceVersion = "original"
	md.Representation.TransformSpec = spec
	md.Representation.PolicyVersion = "p1"
	md.Representation.EncoderVersion = "e1"
	md.ExpiresAt = original.ExpiresAt
	f.commit(t, state, lease, ref, md)
	pinned, err := f.repo.LookupDerivedRead(t.Context(), f.key, original.ID, "original", ref.TransformFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Lease.Close()
	if err = f.repo.EvictReference(t.Context(), source.ID(), original); err != nil {
		t.Fatal(err)
	}
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != 0 {
		t.Fatal("source eviction left dependency reachable", root, err)
	}
	f.collect(t)
	absent(t, source, original.ID)
	if readChunk(t, source, md.ID, 0) != "webp" {
		t.Fatal("product reader lost bytes")
	}
	pinned.Lease.Close()
	f.collect(t)
	absent(t, source, md.ID)
}

func TestManagerActualReopenKeepsMigrationAndOrphanJournal(t *testing.T) {
	f := newManagerFixture(t)
	_, target := f.locations(t)
	_, original := f.publish(t, httpSelector(t, "jpeg"), "original", "original-image", false)
	if err := f.repo.MigrateResource(t.Context(), f.key, target.ID()); err != nil {
		t.Fatal(err)
	}
	_, _, _, orphan := f.candidate(t, httpSelector(t, "png"), "unpublished", "png-body", false)
	f.m.Close()
	for i, b := range f.buckets {
		path := b.Path()
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		f.buckets[i] = &migrationProbe{managedBucket: durableBucket(t, path, "pebble").(managedBucket)}
	}
	f.open(t)
	result, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"jpeg"}})
	if err != nil || result.Resource.Resource.ActiveLocation != target.ID() {
		t.Fatal(result, err)
	}
	if readChunk(t, f.bucket(target.ID()), original.ID, 0) != "orig" {
		t.Fatal("published copy lost")
	}
	if _, err = f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"png"}}); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("journal made orphan readable", err)
	}
	f.collect(t)
	absent(t, f.bucket(target.ID()), orphan.ID)
}
