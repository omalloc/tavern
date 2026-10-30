package storage_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage"
)

func seedLegacyBody(t *testing.T, bucket storagev1.Bucket, id *object.ID, flags object.CacheFlag, keys []string) {
	t.Helper()
	md := &object.Metadata{ID: id, Flags: flags, VirtualKey: keys, Size: 3, BlockSize: 1, Code: 200, Headers: http.Header{"Content-Length": {"3"}}}
	for i := uint32(0); i < 3; i++ {
		writer, _, err := bucket.WriteChunkFile(t.Context(), id, i)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write([]byte{'x'}); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		md.Chunks.Set(i)
	}
	if err := bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCollectionScheduledOptInAndRestartProgress(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart-manual", true: "scheduled"}[scheduled], func(t *testing.T) {
			base := t.TempDir()
			config := &conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring",
				Buckets:        []*conf.Bucket{{Path: filepath.Join(base, "data"), MaxObjectLimit: 100}},
				Representation: &conf.RepresentationStorage{StateDirectory: filepath.Join(base, "control"), GCInterval: time.Hour, LegacyVaryGCFileOps: 2}}
			open := func() storagev1.Storage {
				store, err := storage.New(config, log.DefaultLogger)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				return store
			}
			store := open()
			bucket := store.(storagev1.RepresentationBucketProvider).RepresentationBuckets()[0]
			root := object.NewID("https://example.com/retired")
			child := object.NewVirtualID(root.Path(), "old-child")
			seedLegacyBody(t, bucket, root, object.FlagVaryIndex, []string{child.Ext()})
			seedLegacyBody(t, bucket, child, object.FlagVaryCache, nil)
			if scheduled {
				// The disabled default survives ticks without reclaiming old data.
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				config.Representation.GCInterval = 100 * time.Millisecond
				store = open()
				bucket = store.(storagev1.RepresentationBucketProvider).RepresentationBuckets()[0]
				time.Sleep(250 * time.Millisecond)
				if _, err := bucket.Lookup(t.Context(), child); err != nil {
					t.Fatal("default-off maintenance deleted legacy data", err)
				}
			} else {
				result, err := store.(storagev1.LegacyVaryCollector).CollectLegacyVary(t.Context(), 128, 2)
				if err != nil || result.FileOps < 1 || result.FileOps > 2 || result.Collected != 0 {
					t.Fatal("expected one partially reclaimed object", result, err)
				}
				// The durable root references are already cut before reopening.
				md, err := bucket.Lookup(t.Context(), root)
				if err != nil || len(md.VirtualKey) != 0 {
					t.Fatal("partial cleanup did not revoke root", err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			config.Representation.LegacyVaryGCBudget = 128
			config.Representation.GCInterval = 100 * time.Millisecond
			store = open()
			bucket = store.(storagev1.RepresentationBucketProvider).RepresentationBuckets()[0]
			deadline := time.Now().Add(5 * time.Second)
			for {
				_, rootErr := bucket.Lookup(t.Context(), root)
				_, childErr := bucket.Lookup(t.Context(), child)
				if errors.Is(rootErr, storagev1.ErrKeyNotFound) && errors.Is(childErr, storagev1.ErrKeyNotFound) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("scheduled cleanup failed to resume durable progress", rootErr, childErr)
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, id := range []*object.ID{root, child} {
				for i := uint32(0); i < 3; i++ {
					file, _, err := bucket.ReadChunkFile(t.Context(), id, i)
					if file != nil {
						file.Close()
					}
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatal("metadata removed before physical cleanup", id, i, err)
					}
				}
			}
		})
	}
}
func TestLegacyCollectionBoundedLeaseProtectionAndExactIDs(t *testing.T) {
	for _, dirAware := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "diraware"}[dirAware], func(t *testing.T) {
			store := newPurgeStorage(t, false, dirAware)
			buckets := store.(storagev1.RepresentationBucketProvider).RepresentationBuckets()
			raw := "https://example.com/old?a=1"
			root := object.NewID(raw)
			child := object.NewVirtualID(raw, "legacy-child")
			plain := object.NewVirtualID(raw, "unrelated-plain")
			for _, bucket := range buckets {
				seedLegacyBody(t, bucket, root, object.FlagVaryIndex, []string{child.Ext(), plain.Ext()})
				seedLegacyBody(t, bucket, child, object.FlagVaryCache, nil)
				seedLegacyBody(t, bucket, plain, object.FlagCache, nil)
			}
			lease, err := store.(storagev1.LegacyFillCoordinator).BeginLegacyFill(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			collector := store.(storagev1.LegacyVaryCollector)
			for i := 0; i < 4; i++ {
				result, err := collector.CollectLegacyVary(t.Context(), 128, 2)
				if err != nil || result.Collected != 0 || result.FileOps != 0 {
					t.Fatal("collected active legacy reader/writer", result, err)
				}
			}
			lease.Close()
			total := 0
			for pass := 0; pass < 40; pass++ {
				result, err := collector.CollectLegacyVary(t.Context(), 128, 2)
				if err != nil || result.Scanned > 128 || result.FileOps > 2 {
					t.Fatal(result, err)
				}
				total += result.Collected
				if total == 2*len(buckets) {
					break
				}
			}
			if total != 2*len(buckets) {
				t.Fatal("bounded passes did not finish", total)
			}
			for _, bucket := range buckets {
				for _, id := range []*object.ID{root, child} {
					if _, err = bucket.Lookup(t.Context(), id); !errors.Is(err, storagev1.ErrKeyNotFound) {
						t.Fatal("old metadata retained", err)
					}
					if file, _, err := bucket.ReadChunkFile(t.Context(), id, 0); !errors.Is(err, os.ErrNotExist) {
						if file != nil {
							file.Close()
						}
						t.Fatal("old physical bytes retained", err)
					}
				}
				if _, err = bucket.Lookup(t.Context(), plain); err != nil {
					t.Fatal("followed old VirtualKey into ordinary cache", err)
				}
			}
		})
	}
}
