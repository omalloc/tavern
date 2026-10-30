package representation_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/bucket/memory"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
	"github.com/omalloc/tavern/storage/representation"
	"github.com/omalloc/tavern/storage/sharedkv"
)

type fixture struct {
	repo     *representation.IndexRepository
	bucket   storage.Bucket
	controls storage.ControlStore
	key      object.ResourceKey
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	bucket, err := memory.New(&storage.BucketConfig{Driver: storage.TypeInMemory, Type: storage.TypeHot, MaxObjectLimit: 100}, sharedkv.NewEmpty())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bucket.Close() })
	key, err := object.NewResourceKey("https://example.com/image?a=1&a=2", "http-v2", "upstream-a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	controls := bucket.(storage.ControlStore)
	repo, err := representation.NewIndexRepository(controls, bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
		if location != bucket.ID() {
			return nil, fmt.Errorf("wrong fixed location %q", location)
		}
		return bucket.Lookup(ctx, id)
	}, representation.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(t.Context(), key, nil); err != nil {
		t.Fatal(err)
	}
	return &fixture{repo, bucket, controls, key, now}
}

func (f *fixture) candidate(t *testing.T, fields []string, header http.Header, generation, body string, date int64) (object.VariantRef, *object.Metadata) {
	t.Helper()
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := representation.NewHTTPSelector(fields, header)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.key.RepresentationID(root.Resource.Incarnation, root.Resource.PurgeEpoch, ref.SelectorFingerprint, generation)
	if err != nil {
		t.Fatal(err)
	}
	ref.RepresentationID, ref.ResponseDate = id, date
	md := &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, Size: uint64(len(body)), BlockSize: 64, Code: 200,
		RespUnix: date, ExpiresAt: f.now.Add(time.Hour).Unix(),
		Headers: http.Header{"Vary": {strings.Join(fields, ", ")}, "Date": {time.Unix(date, 0).UTC().Format(http.TimeFormat)}, "Content-Length": {fmt.Sprint(len(body))}, "Content-Type": {body}},
		Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion, ResourceID: f.key.ResourceID(),
			Incarnation: root.Resource.Incarnation, PurgeEpoch: root.Resource.PurgeEpoch, ValidatedEpoch: root.Resource.RevalidateEpoch,
			BodyGeneration: generation, State: object.RepresentationComplete, Kind: object.RepresentationOrigin},
	}
	writer, _, err := f.bucket.WriteChunkFile(t.Context(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	md.Chunks.Set(0)
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	return ref, md
}

func (f *fixture) publish(t *testing.T, refs ...object.VariantRef) {
	t.Helper()
	_, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error {
		root.Variants = append(root.Variants, refs...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) body(t *testing.T, header http.Header) (*representation.LookupResult, string) {
	t.Helper()
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := f.bucket.ReadChunkFile(t.Context(), selected.Representation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return selected, string(body)
}

func TestIndexLookupCartesian(t *testing.T) {
	f := newFixture(t)
	for _, mime := range []string{"image/webp", "image/jpeg"} {
		for _, encoding := range []string{"identity", "gzip"} {
			header := http.Header{"Accept": {mime}, "Accept-Encoding": {encoding}}
			body := mime + ":" + encoding
			ref, _ := f.candidate(t, []string{"Accept", "Accept-Encoding"}, header, body, body, f.now.Unix())
			f.publish(t, ref)
		}
	}
	for range 2 {
		for _, mime := range []string{"image/webp", "image/jpeg"} {
			for _, encoding := range []string{"identity", "gzip"} {
				selected, body := f.body(t, http.Header{"Accept": {mime}, "Accept-Encoding": {encoding}})
				if body != mime+":"+encoding || !selected.Fresh {
					t.Fatalf("wrong selection: %q", body)
				}
			}
		}
	}
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != 4 || len(root.Headers) != 0 {
		t.Fatalf("invalid control root: %v, %v", root, err)
	}
	_, err = f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"image/png"}, "Accept-Encoding": {"gzip"}})
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatalf("unexpected variant matched: %v", err)
	}
}

func TestIndexLookupDateAndFieldSets(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/webp,image/jpeg"}, "Accept-Encoding": {"gzip"}}
	for i, fields := range [][]string{nil, {"Accept"}, {"Accept", "Accept-Encoding"}} {
		body := fmt.Sprint("variant-", i)
		ref, _ := f.candidate(t, fields, header, body, body, f.now.Unix()+int64(i))
		f.publish(t, ref)
	}
	selected, body := f.body(t, header)
	if body != "variant-2" {
		t.Fatal(body)
	}
	if len(selected.Resource.Headers) != 0 || selected.Resource.Resource.Revision != 4 {
		t.Fatal("root/response conflated")
	}
	_, body = f.body(t, http.Header{"Accept": {"image/webp,image/jpeg"}, "Accept-Encoding": {"identity"}})
	if body != "variant-1" {
		t.Fatal(body)
	}
	_, body = f.body(t, http.Header{"Accept": {"image/png"}})
	if body != "variant-0" {
		t.Fatal(body)
	}
	// Removing Vary on a newer response is a new no-Vary selector, not the
	// union of every field ever observed at this URL.
	ref, md := f.candidate(t, nil, header, "removed", "removed-vary", f.now.Unix()+3)
	md.ExpiresAt = f.now.Add(-time.Second).Unix()
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	f.publish(t, ref)
	selected, body = f.body(t, header)
	if body != "removed-vary" || selected.Fresh {
		t.Fatal("older fresh candidate hid newer expired response")
	}
}

func TestIndexLookupStableDateTie(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg"}}
	a, _ := f.candidate(t, []string{"Accept"}, header, "body-a", "a", f.now.Unix())
	b, _ := f.candidate(t, []string{"Accept"}, header, "body-b", "b", f.now.Unix())
	f.publish(t, b, a)
	first, _ := f.repo.Lookup(t.Context(), f.key, header)
	_, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error {
		root.Variants[0], root.Variants[1] = root.Variants[1], root.Variants[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || first.Representation.ID.Hash() != second.Representation.ID.Hash() {
		t.Fatal("tie depends on insertion order")
	}
}

func TestIndexUpdateConcurrent(t *testing.T) {
	f := newFixture(t)
	const count = 12
	refs := make([]object.VariantRef, count)
	for i := range count {
		refs[i], _ = f.candidate(t, []string{"Accept"}, http.Header{"Accept": {fmt.Sprint(i)}}, fmt.Sprint(i), fmt.Sprint(i), f.now.Unix())
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, ref := range refs {
		wg.Go(func() {
			<-start
			_, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.Variants = append(root.Variants, ref); return nil })
			if err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != count || root.Resource.Revision != count+1 {
		t.Fatalf("lost concurrent updates: %v, %v", root, err)
	}
	for i := range count {
		_, body := f.body(t, http.Header{"Accept": {fmt.Sprint(i)}})
		if body != fmt.Sprint(i) {
			t.Fatal(body)
		}
	}
}

func TestReservationBudgetAndRelease(t *testing.T) {
	f := newFixture(t)
	a, _ := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"a"}}, "a", "a", f.now.Unix())
	f.publish(t, a)
	b, _ := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {"b"}})
	c, _ := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {"c"}})
	one, err := f.repo.Reserve(t.Context(), f.key, b, 2)
	if err != nil {
		t.Fatal(err)
	}
	two, err := f.repo.Reserve(t.Context(), f.key, b, 2)
	if err != nil {
		t.Fatal("duplicate consumed another slot:", err)
	}
	ready, err := f.repo.Reserve(t.Context(), f.key, a, 2)
	if err != nil {
		t.Fatal("ready update consumed another slot:", err)
	}
	f.repo.Release(ready)
	if _, err := f.repo.Reserve(t.Context(), f.key, c, 2); !errors.Is(err, representation.ErrSelectorLimit) {
		t.Fatal("pending not included in budget", err)
	}
	f.repo.Release(one)
	f.repo.Release(one)
	if _, err := f.repo.Reserve(t.Context(), f.key, c, 2); !errors.Is(err, representation.ErrSelectorLimit) {
		t.Fatal("first release freed another writer's slot", err)
	}
	f.repo.Release(two)
	three, err := f.repo.Reserve(t.Context(), f.key, c, 2)
	if err != nil {
		t.Fatal("released pending slot leaked", err)
	}
	f.repo.Release(three)
	if _, err := f.repo.Reserve(t.Context(), f.key, c, 0); !errors.Is(err, representation.ErrSelectorLimit) {
		t.Fatal(err)
	}
}

func TestReservationConcurrentBudget(t *testing.T) {
	f := newFixture(t)
	const count = 10
	start := make(chan struct{})
	leases := make(chan *representation.Reservation, count)
	var wg sync.WaitGroup
	for i := range count {
		selector, _ := representation.NewHTTPSelector([]string{"X-Variant"}, http.Header{"X-Variant": {fmt.Sprint(i)}})
		wg.Go(func() {
			<-start
			lease, err := f.repo.Reserve(t.Context(), f.key, selector, 3)
			if err != nil && !errors.Is(err, representation.ErrSelectorLimit) {
				t.Error(err)
			}
			if err == nil {
				leases <- lease
			}
		})
	}
	close(start)
	wg.Wait()
	close(leases)
	if len(leases) != 3 {
		t.Fatalf("accepted %d unique selectors, want 3", len(leases))
	}
	for lease := range leases {
		f.repo.Release(lease)
	}
}

type alteredControls struct {
	storage.ControlStore
	alter func(*object.Metadata)
}

func (c alteredControls) LoadControl(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	md, err := c.ControlStore.LoadControl(ctx, id)
	if err == nil && md.Resource != nil {
		c.alter(md)
	}
	return md, err
}

func TestIndexUnknownSchemaAndMissingChild(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg"}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "a", "image", f.now.Unix())
	f.publish(t, ref)
	controls := alteredControls{f.controls, func(md *object.Metadata) { md.Resource.SchemaVersion++ }}
	repo, err := representation.NewIndexRepository(controls, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("unknown schema was not a miss", err)
	}
	if _, err := repo.Update(t.Context(), f.key, nil); !errors.Is(err, representation.ErrUnsupportedSchema) {
		t.Fatal("unknown schema could be overwritten", err)
	}
	if err := f.bucket.Discard(t.Context(), md.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("dangling reference was readable", err)
	}
}

func TestIndexRejectsUnpublishedAndCorruptChildren(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg"}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "a", "image", f.now.Unix())
	md.Representation.State = object.RepresentationFilling
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.Variants = append(root.Variants, ref); return nil }); !errors.Is(err, representation.ErrInvalidRecord) {
		t.Fatal("published incomplete child", err)
	}
	md.Representation.State = object.RepresentationComplete
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	f.publish(t, ref)
	// A supplied fingerprint cannot replace the full stored snapshot.
	controls := alteredControls{f.controls, func(root *object.Metadata) { root.Resource.Variants[0].SelectorSnapshot.Fields[0].Values[0] = "other" }}
	repo, _ := representation.NewIndexRepository(controls, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	})
	if _, err := repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("fingerprint alone matched", err)
	}
	md.Representation.BodyGeneration = "wrong-generation"
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("wrong generation was readable", err)
	}
}

type failingControls struct{ storage.ControlStore }

func (c failingControls) StoreControl(context.Context, *object.Metadata) error {
	return errors.New("control write failed")
}

func TestIndexUpdateFailureAndOwnership(t *testing.T) {
	f := newFixture(t)
	before, err := f.repo.Load(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := representation.NewIndexRepository(failingControls{f.controls}, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.RevalidateEpoch++; return nil }); err == nil {
		t.Fatal("failed control write was confirmed")
	}
	after, err := f.repo.Load(t.Context(), f.key)
	if err != nil || before.Resource.Revision != after.Resource.Revision || after.Resource.RevalidateEpoch != 0 {
		t.Fatal("failed update changed control state", err)
	}
	var retained *object.ResourceRecord
	published, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { retained = root; root.RevalidateEpoch++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	retained.RevalidateEpoch = 99
	published.Resource.RevalidateEpoch = 100
	loaded, err := f.repo.Load(t.Context(), f.key)
	if err != nil || loaded.Resource.RevalidateEpoch != 1 {
		t.Fatal("callback or result aliases stored root", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := f.repo.Update(ctx, f.key, func(root *object.ResourceRecord) error { root.RevalidateEpoch++; cancel(); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled update was confirmed", err)
	}
	loaded, err = f.repo.Load(t.Context(), f.key)
	if err != nil || loaded.Resource.RevalidateEpoch != 1 {
		t.Fatal("cancelled update changed control state", err)
	}
}

func TestIndexRejectsPartialAndUnknownLength(t *testing.T) {
	f := newFixture(t)
	ref, md := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"image/jpeg"}}, "a", "image", f.now.Unix())
	for _, mutate := range []func(*object.Metadata){
		func(md *object.Metadata) { md.Headers.Del("Content-Length") },
		func(md *object.Metadata) { md.Headers.Set("Content-Length", "999") },
		func(md *object.Metadata) { md.Headers.Set("Content-Range", "bytes 0-4/10") },
		func(md *object.Metadata) { md.Flags = object.FlagChunkedCache },
		func(md *object.Metadata) { md.BlockSize = 1 << 63 },
	} {
		copy := md.Clone()
		mutate(copy)
		if err := f.bucket.Store(t.Context(), copy); err != nil {
			t.Fatal(err)
		}
		if _, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.Variants = append(root.Variants, ref); return nil }); !errors.Is(err, representation.ErrInvalidRecord) {
			t.Fatal("partial/unknown-length response published", err)
		}
	}
	selector, _ := representation.NewHTTPSelector(nil, nil)
	if _, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.Variants = append(root.Variants, selector); return nil }); !errors.Is(err, representation.ErrInvalidRecord) {
		t.Fatal("reference without an ID was accepted", err)
	}
}
