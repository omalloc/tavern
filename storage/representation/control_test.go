package representation_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/disk"
	"github.com/omalloc/tavern/storage/bucket/empty"
	"github.com/omalloc/tavern/storage/bucket/memory"
	"github.com/omalloc/tavern/storage/sharedkv"
)

func TestControlRecordProtection(t *testing.T) {
	for _, name := range []string{"memory", "disk"} {
		t.Run(name, func(t *testing.T) {
			kv := sharedkv.NewMemSharedKV()
			t.Cleanup(func() { _ = kv.Close() })
			var bucket storage.Bucket
			var err error
			if name == "memory" {
				bucket, err = memory.New(&storage.BucketConfig{Driver: storage.TypeInMemory, MaxObjectLimit: 100}, kv)
			} else {
				path := t.TempDir()
				bucket, err = disk.New(&storage.BucketConfig{Path: path, DBPath: filepath.Join(path, ".index"), DBType: "pebble", MaxObjectLimit: 100}, kv)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bucket.Close() })
			controls, ok := bucket.(storage.ControlStore)
			if !ok {
				t.Fatal("missing control capability")
			}
			key, err := object.NewResourceKey("https://example.com/image", "http-v2", "origin")
			if err != nil {
				t.Fatal(err)
			}
			root := &object.Metadata{ID: key.ID(), RecordKind: object.RecordResource, Resource: &object.ResourceRecord{
				SchemaVersion: object.RepresentationSchemaVersion, Revision: 1, ResourceID: key.ResourceID(),
				CanonicalURL: key.CanonicalURL(), Namespace: key.Namespace(), Incarnation: "inc", ActiveLocation: bucket.ID(),
			}}
			if err := controls.StoreControl(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			root.Resource.Revision++
			if err := controls.StoreControl(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			if bucket.Objects() != 0 || bucket.Exist(t.Context(), root.ID.Bytes()) {
				t.Fatal("control counted as an ordinary object")
			}
			if _, err := bucket.Lookup(t.Context(), root.ID); !errors.Is(err, storage.ErrKeyNotFound) {
				t.Fatal("control was returned as a response", err)
			}
			bucket.Touch(t.Context(), root.ID)
			if err := bucket.Store(t.Context(), root); !errors.Is(err, storage.ErrProtectedControl) {
				t.Fatal("ordinary store accepted control", err)
			}
			for _, operation := range []func() error{
				func() error { return bucket.Remove(t.Context(), root.ID) },
				func() error { return bucket.Discard(t.Context(), root.ID) },
				func() error { return bucket.DiscardWithHash(t.Context(), root.ID.Hash()) },
				func() error { return bucket.DiscardWithMetadata(t.Context(), root) },
				func() error { return bucket.Migrate(t.Context(), root.ID, bucket) },
			} {
				if err := operation(); !errors.Is(err, storage.ErrProtectedControl) {
					t.Fatal("ordinary lifecycle modified control", err)
				}
			}
			if _, _, err := bucket.WriteChunkFile(t.Context(), root.ID, 0); !errors.Is(err, storage.ErrProtectedControl) {
				t.Fatal("opened a control body writer", err)
			}
			if _, _, err := bucket.ReadChunkFile(t.Context(), root.ID, 0); !errors.Is(err, storage.ErrProtectedControl) {
				t.Fatal("opened a control body reader", err)
			}
			count := 0
			if err := bucket.Iterate(t.Context(), func(*object.Metadata) error { count++; return nil }); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("control entered ordinary scans")
			}
			count = 0
			if err := kv.Iterate(t.Context(), func(_, _ []byte) error { count++; return nil }); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("control changed shared counters or ordinary directory entries")
			}
			loaded, err := controls.LoadControl(t.Context(), root.ID)
			if err != nil || loaded.Resource.Revision != 2 || loaded.HasComplete() {
				t.Fatalf("control damaged: %v, %v", loaded, err)
			}
			invalid := root.Clone()
			invalid.Headers = map[string][]string{"Content-Type": {"image/jpeg"}}
			if err := controls.StoreControl(t.Context(), invalid); !errors.Is(err, storage.ErrProtectedControl) {
				t.Fatal("control admitted response headers", err)
			}
		})
	}
	emptyBucket, err := empty.New(&storage.BucketConfig{}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := emptyBucket.(storage.ControlStore); ok {
		t.Fatal("empty bucket claims control persistence")
	}
}
