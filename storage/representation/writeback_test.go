package representation_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

type writebackProbe struct {
	managedBucket
	cacheSyncs, barriers atomic.Int32
	failBarrier          atomic.Bool
}

func (b *writebackProbe) SyncRepresentationData(context.Context, *object.Metadata) error {
	b.cacheSyncs.Add(1)
	return errors.New("writeback attempted synchronous file flush")
}
func (b *writebackProbe) StoreRepresentationSync(context.Context, *object.Metadata) error {
	b.cacheSyncs.Add(1)
	return errors.New("writeback attempted synchronous child flush")
}
func (b *writebackProbe) StoreRepresentationJournalSync(context.Context, *object.Metadata) error {
	b.cacheSyncs.Add(1)
	return errors.New("writeback attempted synchronous chunk journal")
}
func (b *writebackProbe) StoreControlSync(ctx context.Context, md *object.Metadata) error {
	b.barriers.Add(1)
	if b.failBarrier.Load() {
		return errors.New("injected invalidation sync failure")
	}
	return b.managedBucket.StoreControlSync(ctx, md)
}

func useWriteback(t *testing.T, f *fixture, bucket storage.Bucket) {
	t.Helper()
	repo, err := representation.NewIndexRepository(bucket.(storage.ControlStore), bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
		return bucket.Lookup(ctx, id)
	}, representation.WithDurability(bucket), representation.WithWriteback(), representation.WithClock(func() time.Time { return f.now }))
	if err != nil {
		t.Fatal(err)
	}
	f.repo, f.bucket, f.controls = repo, bucket, bucket.(storage.ControlStore)
}

func TestWritebackPublishesWithoutCacheSyncButRequiresPurgeSync(t *testing.T) {
	f := diskFixture(t, "")
	probe := &writebackProbe{managedBucket: f.bucket.(managedBucket)}
	useWriteback(t, f, probe)
	f.key, _ = object.NewResourceKey("https://example.com/async", "http-v2", "origin")
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	ref, md := f.candidate(t, nil, nil, "body", "complete", f.now.Unix())
	md.Representation.StorageSelectorFingerprint = ref.SelectorFingerprint
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.BindFill(t.Context(), state, lease, md); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.JournalFill(t.Context(), state, lease, probe, md); err != nil {
		t.Fatal(err)
	}
	published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return probe.Store(t.Context(), md) })
	if err != nil || !published || probe.cacheSyncs.Load() != 0 || probe.barriers.Load() != 0 {
		t.Fatal("ordinary fill waited for persistence", published, err, probe.cacheSyncs.Load(), probe.barriers.Load())
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, nil); err != nil {
		t.Fatal("unflushed cache was not immediately readable", err)
	}
	probe.failBarrier.Store(true)
	if err := f.repo.Purge(t.Context(), f.key.CanonicalURL(), storage.PurgeControl{Hard: true}); err == nil {
		t.Fatal("failed barrier persistence acknowledged PURGE")
	}
	probe.failBarrier.Store(false)
	if err := f.repo.Purge(t.Context(), f.key.CanonicalURL(), storage.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	if probe.barriers.Load() != 2 {
		t.Fatal("PURGE did not use reliable barrier writes", probe.barriers.Load())
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, nil); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("purged cache remained readable", err)
	}
}

func TestWritebackCrashWriter(t *testing.T) {
	base, mode := os.Getenv("TAVERN_WRITEBACK_CRASH_DIR"), os.Getenv("TAVERN_WRITEBACK_CRASH_MODE")
	if base == "" {
		t.Skip("crash subprocess only")
	}
	f := diskFixture(t, base)
	useWriteback(t, f, f.bucket)
	ref, md := f.candidate(t, nil, nil, "old", "old-body", f.now.Unix())
	commitCandidate(t, f, ref, md)
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a committed pre-PURGE root which may survive a crash. A later lost
	// asynchronous root must never make this old representation fresh again.
	if err := f.controls.(storage.DurableControlStore).StoreControlSync(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lateRef, lateMD := f.candidate(t, nil, nil, "late", "late-body", f.now.Unix())
	lease, err := f.repo.ReserveFor(t.Context(), state, lateRef, 2)
	if err != nil {
		t.Fatal(err)
	}
	control := storage.PurgeControl{Hard: strings.HasPrefix(mode, "hard"), Dir: strings.HasSuffix(mode, "prefix")}
	target := f.key.CanonicalURL()
	if control.Dir {
		target = "https://example.com/img/"
	}
	if strings.HasSuffix(mode, "resource") {
		kind := representation.InvalidateSoft
		if control.Hard {
			kind = representation.InvalidateHard
		}
		err = f.repo.Invalidate(t.Context(), f.key, kind)
	} else {
		err = f.repo.Purge(t.Context(), target, control)
	}
	if err != nil {
		t.Fatal(err)
	}
	published, err := f.repo.Publish(t.Context(), state, lease, lateRef, lateMD, nil, func() error {
		return errors.New("pre-PURGE fill touched final metadata")
	})
	if err != nil || published {
		t.Fatal("late pre-PURGE generation published", published, err)
	}
	if _, err := f.repo.Capture(t.Context(), f.key); err != nil {
		t.Fatal(err)
	}
	// Do not execute Close, Flush or testing cleanup hooks.
	os.Exit(0)
}

func TestWritebackPurgeSurvivesAbruptExit(t *testing.T) {
	for _, kind := range []string{"hard", "soft"} {
		for _, target := range []string{"exact", "prefix", "resource"} {
			t.Run(kind+"/"+target, func(t *testing.T) {
				base := t.TempDir()
				cmd := exec.Command(os.Args[0], "-test.run=^TestWritebackCrashWriter$")
				cmd.Env = append(os.Environ(), "TAVERN_WRITEBACK_CRASH_DIR="+base, "TAVERN_WRITEBACK_CRASH_MODE="+kind+"/"+target)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("crash writer: %v\n%s", err, output)
				}
				f := diskFixture(t, base)
				useWriteback(t, f, f.bucket)
				selected, err := f.repo.Lookup(t.Context(), f.key, nil)
				if kind == "hard" {
					if !errors.Is(err, storage.ErrKeyNotFound) {
						t.Fatal("hard-purged content resurrected after restart", selected, err)
					}
				} else if err == nil && selected.Fresh {
					t.Fatal("soft-purged content became fresh after restart")
				} else if err != nil && !errors.Is(err, storage.ErrKeyNotFound) {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestWritebackRecoveryRejectsLostOrTornDataAndCollectsUnindexedFiles(t *testing.T) {
	for _, damage := range []string{"missing", "truncated", "same-length", "metadata"} {
		t.Run(damage, func(t *testing.T) {
			base, stateDir := t.TempDir(), t.TempDir()
			bucket := durableBucket(t, base, "pebble")
			open := func(bucket storage.Bucket) *representation.Manager {
				t.Helper()
				manager, err := representation.NewManager(t.Context(), []storage.Bucket{bucket}, representation.ManagerConfig{Durable: true, StateDirectory: stateDir})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { manager.Close() })
				return manager
			}
			manager := open(bucket)
			key, _ := object.NewResourceKey("https://example.com/recovery", "http-v2", "origin")
			repo, _ := manager.Repository(key)
			state, err := repo.Capture(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			f := &fixture{repo: repo, bucket: bucket, controls: bucket.(storage.ControlStore), key: key, now: time.Now()}
			ref, md := f.candidate(t, nil, nil, "body", "original", f.now.Unix())
			md.Representation.StorageSelectorFingerprint = ref.SelectorFingerprint
			md.Representation.RecordChunkDigest(0, []byte("original"))
			lease, err := repo.ReserveFor(t.Context(), state, ref, 2)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.BindFill(t.Context(), state, lease, md); err != nil {
				t.Fatal(err)
			}
			ok, err := repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return bucket.Store(t.Context(), md) })
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			path := md.ID.WPathSlice(base, 0)
			switch damage {
			case "missing":
				err = os.Remove(path)
			case "truncated":
				err = os.WriteFile(path, []byte("x"), 0600)
			case "same-length":
				err = os.WriteFile(path, []byte("corrupt!"), 0600)
			case "metadata":
				err = bucket.Remove(t.Context(), md.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Simulate a completed file whose entire metadata batch was lost.
			orphan, _ := key.RepresentationID(state.Resource().Resource.Incarnation, 0, ref.SelectorFingerprint, "lost-index")
			writer, _, err := bucket.WriteChunkFile(t.Context(), orphan, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(writer, "orphan"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := manager.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(orphan.WPathSlice(base, 0)); err != nil {
				t.Fatal("live recovery removed an unindexed writer's file", err)
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			if err := bucket.Close(); err != nil {
				t.Fatal(err)
			}
			bucket = durableBucket(t, base, "pebble")
			manager = open(bucket)
			repo, _ = manager.Repository(key)
			if selected, err := repo.Lookup(t.Context(), key, nil); !errors.Is(err, storage.ErrKeyNotFound) {
				t.Fatal("damaged writeback cache recovered as a hit", selected, err)
			}
			if _, err := os.Stat(orphan.WPathSlice(base, 0)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unindexed file survived recovery", err)
			}
			if damage == "metadata" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("lost child index leaked its body", err)
				}
			}
			root, err := repo.Load(t.Context(), key)
			if err != nil || len(root.Resource.Variants) != 0 {
				t.Fatal(fmt.Sprint("recovery retained an invalid reference: ", root, err))
			}
		})
	}
}
