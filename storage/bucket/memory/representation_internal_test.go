package memory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
	"github.com/omalloc/tavern/storage/sharedkv"
)

func TestRepresentationEvictionRequiresCoordinator(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbound", true: "callback-failure"}[registered], func(t *testing.T) {
			raw, err := New(&storage.BucketConfig{MaxObjectLimit: 100}, sharedkv.NewEmpty())
			if err != nil {
				t.Fatal(err)
			}
			b := raw.(*memoryBucket)
			defer b.Close()
			key, _ := object.NewResourceKey("https://example.com/a", "v2", "")
			id, _ := key.RepresentationID("inc", 0, object.TransformFingerprint([]byte("s"), "p", "e"), "body")
			md := &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, Size: 4, BlockSize: 4, Code: 200, Headers: make(http.Header), Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion, Kind: object.RepresentationOrigin, State: object.RepresentationComplete}}
			w, _, err := b.WriteChunkFile(t.Context(), id, 0)
			if err != nil {
				t.Fatal(err)
			}
			io.WriteString(w, "body")
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			md.Chunks.Set(0)
			if err = b.Store(t.Context(), md); err != nil {
				t.Fatal(err)
			}
			callback := make(chan struct{}, 1)
			if registered {
				b.SetRepresentationEvictor(func(context.Context, *object.Metadata) error {
					callback <- struct{}{}
					return errors.New("injected coordinator failure")
				})
			}
			if b.cache.Evict(1) != 1 {
				t.Fatal("eviction not queued")
			}
			if registered {
				select {
				case <-callback:
				case <-time.After(time.Second):
					t.Fatal("coordinator bypassed")
				}
			}
			// Close joins the eviction loop; it must not use legacy direct deletion.
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
			r, _, err := b.ReadChunkFile(t.Context(), id, 0)
			if err != nil {
				t.Fatal("unsafe fallback deleted bytes", err)
			}
			defer r.Close()
		})
	}
}
