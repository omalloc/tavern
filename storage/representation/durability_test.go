package representation_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/disk"
	_ "github.com/omalloc/tavern/storage/indexdb/nutsdb"
	"github.com/omalloc/tavern/storage/representation"
	"github.com/omalloc/tavern/storage/sharedkv"
)

func durableBucket(t *testing.T, base, backend string) storage.Bucket {
	t.Helper()
	if base == "" {
		base = t.TempDir()
	}
	bucket, err := disk.New(&storage.BucketConfig{Path: base, DBPath: filepath.Join(base, ".index"), DBType: backend, Driver: "native", MaxObjectLimit: 100,
		DBConfig: map[string]any{"write_sync_mode": false}}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bucket.Close() })
	return bucket
}

func durableRepo(t *testing.T, controls storage.ControlStore, bucket storage.Bucket) *representation.IndexRepository {
	t.Helper()
	repo, err := representation.NewIndexRepository(controls, bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
		if location != bucket.ID() {
			return nil, storage.ErrKeyNotFound
		}
		return bucket.Lookup(ctx, id)
	}, representation.WithDurability(bucket), representation.WithClock(func() time.Time { return time.Unix(1800000000, 0) }))
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func diskFixture(t *testing.T, base string) *fixture {
	t.Helper()
	bucket := durableBucket(t, base, "pebble")
	controls := bucket.(storage.ControlStore)
	repo := durableRepo(t, controls, bucket)
	key, _ := object.NewResourceKey("https://example.com/img/a", "http-v2", "origin")
	if _, err := repo.Capture(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	return &fixture{repo: repo, bucket: bucket, controls: controls, key: key, now: time.Unix(1800000000, 0)}
}

func commitCandidate(t *testing.T, f *fixture, ref object.VariantRef, md *object.Metadata) {
	t.Helper()
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 10)
	if err != nil {
		t.Fatal(err)
	}
	published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return f.bucket.Store(t.Context(), md) })
	if err != nil || !published {
		t.Fatal("durable commit", published, err)
	}
}

type durableEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *durableEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}
func (e *durableEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}
func (e *durableEvents) reset() { e.mu.Lock(); defer e.mu.Unlock(); e.events = nil }

type probedControls struct {
	storage.DurableControlStore
	events *durableEvents
	fail   atomic.Bool
}

func (c *probedControls) StoreControl(context.Context, *object.Metadata) error {
	return errors.New("durable coordinator used ordinary control write")
}
func (c *probedControls) StoreControlSync(ctx context.Context, md *object.Metadata) error {
	c.events.add("root")
	if c.fail.Load() {
		return errors.New("injected control sync failure")
	}
	return c.DurableControlStore.StoreControlSync(ctx, md)
}

type probedBucket struct {
	storage.DurableRepresentationBucket
	events              *durableEvents
	failData, failChild atomic.Bool
	entered             chan struct{}
	release             chan struct{}
}

func (b *probedBucket) SyncRepresentationData(ctx context.Context, md *object.Metadata) error {
	b.events.add("data")
	if b.entered != nil {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if b.failData.Load() {
		return errors.New("injected file/directory sync failure")
	}
	return b.DurableRepresentationBucket.SyncRepresentationData(ctx, md)
}
func (b *probedBucket) StoreRepresentationSync(ctx context.Context, md *object.Metadata) error {
	b.events.add("child")
	if b.failChild.Load() {
		return errors.New("injected child metadata sync failure")
	}
	return b.DurableRepresentationBucket.StoreRepresentationSync(ctx, md)
}

func TestDurablePublicationOrderAndFailures(t *testing.T) {
	for _, failure := range []string{"none", "data", "child", "root"} {
		t.Run(failure, func(t *testing.T) {
			f := diskFixture(t, "")
			events := &durableEvents{}
			controls := &probedControls{DurableControlStore: f.controls.(storage.DurableControlStore), events: events}
			bucket := &probedBucket{DurableRepresentationBucket: f.bucket.(storage.DurableRepresentationBucket), events: events}
			f.repo = durableRepo(t, controls, bucket)
			ref, md := f.candidate(t, nil, nil, "generation", "body", f.now.Unix())
			state, err := f.repo.Capture(t.Context(), f.key)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
			if err != nil {
				t.Fatal(err)
			}
			events.reset()
			bucket.failData.Store(failure == "data")
			bucket.failChild.Store(failure == "child")
			controls.fail.Store(failure == "root")
			published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { events.add("ordinary-child"); return bucket.Store(t.Context(), md) })
			if failure == "none" {
				if err != nil || !published || !reflect.DeepEqual(events.snapshot(), []string{"data", "ordinary-child", "child", "root"}) {
					t.Fatal(published, err, events.snapshot())
				}
				if _, err := f.repo.Lookup(t.Context(), f.key, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || published {
					t.Fatal("sync failure acknowledged publication", published, err)
				}
				if _, err := f.repo.Lookup(t.Context(), f.key, nil); !errors.Is(err, storage.ErrKeyNotFound) {
					t.Fatal("uncommitted body readable", err)
				}
				if failure == "data" && !reflect.DeepEqual(events.snapshot(), []string{"data"}) {
					t.Fatal("child written after failed file sync", events.snapshot())
				}
				// A failed commit releases the pending selector even when its
				// unreferenced files are deliberately left for bounded GC.
				other, _ := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {"other"}})
				lease, err := f.repo.ReserveFor(t.Context(), state, other, 1)
				if err != nil {
					t.Fatal("failed commit leaked reservation", err)
				}
				f.repo.Release(lease)
			}
		})
	}
}

func TestDurableValidationRootFailureIsMissAndUpdateCannotBypassCommit(t *testing.T) {
	f := diskFixture(t, "")
	ref, md := f.candidate(t, nil, nil, "original", "body", f.now.Unix())
	if _, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.Variants = append(root.Variants, ref); return nil }); !errors.Is(err, representation.ErrInvalidRecord) {
		t.Fatal("Update bypassed durable publication", err)
	}
	commitCandidate(t, f, ref, md)
	selected, err := f.repo.Lookup(t.Context(), f.key, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := &durableEvents{}
	controls := &probedControls{DurableControlStore: f.controls.(storage.DurableControlStore), events: events}
	f.repo = durableRepo(t, controls, f.bucket)
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, selected.Reference, 1)
	if err != nil {
		t.Fatal(err)
	}
	updated := selected.Representation.Clone()
	updated.ExpiresAt += 3600
	controls.fail.Store(true)
	published, err := f.repo.Publish(t.Context(), state, lease, selected.Reference, updated, &selected.Reference, func() error { return f.bucket.Store(t.Context(), updated) })
	if err == nil || published {
		t.Fatal("failed durable validation was confirmed", published, err)
	}
	// The new child revision is durable, but no root acknowledged it. Even
	// equal Date/physical ID cannot give the old root an uncommitted new TTL.
	recovered := durableRepo(t, f.controls, f.bucket)
	if _, err := recovered.Lookup(t.Context(), f.key, nil); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("uncommitted child refreshed old root", err)
	}
	file, _, err := f.bucket.ReadChunkFile(t.Context(), selected.Representation.ID, 0)
	if err != nil {
		t.Fatal("failed validation removed an existing body", err)
	}
	file.Close()
}

func TestDurableDataSyncDoesNotHoldInvalidationLock(t *testing.T) {
	f := diskFixture(t, "")
	events := &durableEvents{}
	bucket := &probedBucket{DurableRepresentationBucket: f.bucket.(storage.DurableRepresentationBucket), events: events, entered: make(chan struct{}), release: make(chan struct{})}
	f.repo = durableRepo(t, f.controls, bucket)
	ref, md := f.candidate(t, nil, nil, "generation", "body", f.now.Unix())
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return errors.New("superseded fill touched child") })
		if published {
			err = errors.New("superseded fill published")
		}
		done <- err
	}()
	select {
	case <-bucket.entered:
	case <-time.After(10 * time.Second):
		close(bucket.release)
		t.Fatal("data sync not entered")
	}
	invalidated := make(chan error, 1)
	go func() {
		invalidated <- f.repo.InvalidatePrefix(t.Context(), "https://example.com/img/", representation.InvalidateHard)
	}()
	select {
	case err := <-invalidated:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		close(bucket.release)
		t.Fatal("file I/O held the invalidation lock")
	}
	close(bucket.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDurableRepositoryRejectsVolatileAndUnsupportedBackends(t *testing.T) {
	f := newFixture(t)
	lookup := func(context.Context, string, *object.ID) (*object.Metadata, error) {
		return nil, storage.ErrKeyNotFound
	}
	if _, err := representation.NewIndexRepository(f.controls, f.bucket.ID(), lookup, representation.WithDurability(f.bucket)); !errors.Is(err, storage.ErrDurabilityUnsupported) {
		t.Fatal("memory claims persistent durability", err)
	}
	nuts := durableBucket(t, "", "nutsdb")
	if _, err := representation.NewIndexRepository(nuts.(storage.ControlStore), nuts.ID(), lookup, representation.WithDurability(nuts)); !errors.Is(err, storage.ErrDurabilityUnsupported) {
		t.Fatal("asynchronous NutsDB claims persistent durability", err)
	}
}

func TestDurableCrashWriter(t *testing.T) {
	base := os.Getenv("TAVERN_DURABILITY_CRASH_DIR")
	if base == "" {
		t.Skip("crash subprocess only")
	}
	f := diskFixture(t, base)
	ref, md := f.candidate(t, nil, nil, "a", "body-a", f.now.Unix())
	commitCandidate(t, f, ref, md)
	f.key, _ = object.NewResourceKey("https://example.com/image/b", "http-v2", "origin")
	if _, err := f.repo.Capture(t.Context(), f.key); err != nil {
		t.Fatal(err)
	}
	ref, md = f.candidate(t, nil, nil, "b", "body-b", f.now.Unix())
	commitCandidate(t, f, ref, md)
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/img/", representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/blocked/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	cold, _ := object.NewResourceKey("https://example.com/gone", "http-v2", "origin")
	if err := f.repo.Invalidate(t.Context(), cold, representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	// Deliberately bypass Close/Flush and all testing cleanup hooks.
	os.Exit(0)
}

func TestDurableCrashRecoveryAndMissingFiles(t *testing.T) {
	base := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDurableCrashWriter$")
	cmd.Env = append(os.Environ(), "TAVERN_DURABILITY_CRASH_DIR="+base)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash writer: %v\n%s", err, output)
	}
	f := diskFixture(t, base)
	selected, err := f.repo.Lookup(t.Context(), f.key, nil)
	if err != nil || selected.Fresh {
		t.Fatal("acknowledged soft barrier lost after process death", selected, err)
	}
	f.key, _ = object.NewResourceKey("https://example.com/image/b", "http-v2", "origin")
	selected, err = f.repo.Lookup(t.Context(), f.key, nil)
	if err != nil || !selected.Fresh {
		t.Fatal("unaffected durable representation lost", selected, err)
	}
	if err := os.Remove(selected.Representation.ID.WPathSlice(base, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, nil); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("missing data was recovered from metadata alone", err)
	}
	for _, raw := range []string{"https://example.com/blocked/cold", "https://example.com/gone"} {
		key, _ := object.NewResourceKey(raw, "http-v2", "origin")
		state, err := f.repo.Capture(t.Context(), key)
		if err != nil || state.Resource().Resource.PurgeEpoch != 1 {
			t.Fatal("acknowledged hard barrier lost", raw, err)
		}
	}
}
