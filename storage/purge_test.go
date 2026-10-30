package storage_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/storage"
	"github.com/omalloc/tavern/storage/representation"
)

func newPurgeStorage(t *testing.T, migration, dirAware bool) storagev1.Storage {
	t.Helper()
	base := t.TempDir()
	s, err := storage.New(&conf.Storage{Driver: "native", DBType: "pebble", DBPath: ".index", SelectionPolicy: "hashring",
		DirAware: &conf.DirAware{Enabled: dirAware}, Migration: &conf.Migration{Enabled: migration, Promote: conf.Promote{MinHits: 1000000, Window: time.Minute}},
		Buckets: []*conf.Bucket{{Path: filepath.Join(base, "warm"), Type: storagev1.TypeWarm, MaxObjectLimit: 100},
			{Path: filepath.Join(base, "hot"), Type: storagev1.TypeHot, MaxObjectLimit: 100}}}, log.DefaultLogger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedPurgeMetadata(t *testing.T, b storagev1.Bucket, id *object.ID) {
	t.Helper()
	err := b.Store(t.Context(), &object.Metadata{ID: id, Size: 5, BlockSize: 64, Code: 200, Headers: http.Header{"Content-Length": {"5"}, "Cache-Control": {"max-age=600"}}, ExpiresAt: time.Now().Unix() + 600})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPurgeVisitsAllBucketsAndLegacyVariants(t *testing.T) {
	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "migrator"}[migration], func(t *testing.T) {
			s := newPurgeStorage(t, migration, false)
			ids := []*object.ID{object.NewID("https://example.com/img/a"), object.NewVirtualID("https://example.com/img/a", `accept:image/webp,image/jpeg;q=0.8`)}
			other := object.NewID("https://example.com/image/a")
			for _, b := range s.Buckets() {
				for _, id := range ids {
					seedPurgeMetadata(t, b, id)
				}
				seedPurgeMetadata(t, b, other)
			}
			if err := s.PURGE("https://EXAMPLE.com/img/", storagev1.PurgeControl{Dir: true, MarkExpired: true}); err != nil {
				t.Fatal(err)
			}
			for _, b := range s.Buckets() {
				for _, id := range ids {
					md, err := b.Lookup(t.Context(), id)
					if err != nil || md.ExpiresAt >= time.Now().Unix() {
						t.Fatal("directory soft purge skipped a copy", b.ID(), md, err)
					}
				}
			}
			if err := s.PURGE("https://example.com/img/a", storagev1.PurgeControl{Hard: true}); err != nil {
				t.Fatal(err)
			}
			for _, b := range s.Buckets() {
				for _, id := range ids {
					if _, err := b.Lookup(t.Context(), id); !errors.Is(err, storagev1.ErrKeyNotFound) {
						t.Fatal("file purge skipped actual child ID", b.ID(), id, err)
					}
				}
				if _, err := b.Lookup(t.Context(), other); err != nil {
					t.Fatal("crossed directory boundary", err)
				}
			}
		})
	}
}

type failedPurger struct{}

func (failedPurger) Purge(context.Context, string, storagev1.PurgeControl) error {
	return storagev1.ErrInvalidationBudget
}

func TestPurgeRoutesColdNamespacesAndFailsOnBarrierFailure(t *testing.T) {
	s := newPurgeStorage(t, false, false)
	b := s.Buckets()[0]
	repo, err := representation.NewIndexRepository(b.(storagev1.ControlStore), b.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) { return b.Lookup(ctx, id) }, representation.WithDurability(b))
	if err != nil {
		t.Fatal(err)
	}
	registry := s.(storagev1.RepresentationRegistry)
	if err := registry.RegisterRepresentationPurger("home", repo); err != nil {
		t.Fatal(err)
	}
	keys := []object.ResourceKey{}
	states := []*representation.IndexState{}
	for _, ns := range []string{"http-v2", "managed-v2"} {
		key, _ := object.NewResourceKey("https://example.com/cold?x=1", ns, "origin")
		keys = append(keys, key)
		state, err := repo.Capture(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
	}
	if err := s.PURGE("https://example.com/cold?x=1", storagev1.PurgeControl{Hard: true}); err != nil {
		t.Fatal("cold invalidation not acknowledged", err)
	}
	selector, _ := representation.NewHTTPSelector(nil, nil)
	for i, key := range keys {
		if _, err := repo.ReserveFor(t.Context(), states[i], selector, 1); !errors.Is(err, representation.ErrSuperseded) {
			t.Fatal("old namespace ticket survived", err)
		}
		state, err := repo.Capture(t.Context(), key)
		if err != nil || state.Resource().Resource.PurgeEpoch != 1 {
			t.Fatal(err)
		}
	}
	if err := registry.RegisterRepresentationPurger("failed", failedPurger{}); err != nil {
		t.Fatal(err)
	}
	if err := s.PURGE("https://example.com/another-cold", storagev1.PurgeControl{Hard: true}); !errors.Is(err, storagev1.ErrInvalidationBudget) {
		t.Fatal("failed backend reported success", err)
	}
}

func TestLegacyPurgeFencesColdFillAndRenewal(t *testing.T) {
	s := newPurgeStorage(t, false, true)
	coordinator := s.(storagev1.LegacyFillCoordinator)
	old, err := coordinator.BeginLegacyFill(t.Context(), "https://example.com/img/cold")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	other, err := coordinator.BeginLegacyFill(t.Context(), "https://example.com/image/other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := s.PURGE("https://example.com/img/", storagev1.PurgeControl{Dir: true, Hard: true}); err != nil {
		t.Fatal("cold active fill was missed", err)
	}
	called := false
	if err := old.Guard(t.Context(), func() error { called = true; return nil }); !errors.Is(err, storagev1.ErrFillSuperseded) || called {
		t.Fatal("old write crossed purge", err)
	}
	if err := other.Guard(t.Context(), func() error { return nil }); err != nil {
		t.Fatal("unrelated fill fenced", err)
	}
	fresh, usable, err := old.Renew(t.Context())
	if err != nil || usable {
		t.Fatal("renew reused invalidated metadata", err)
	}
	defer fresh.Close()
	if err := fresh.Guard(t.Context(), func() error { return nil }); err != nil {
		t.Fatal("new fill could not proceed", err)
	}
}
