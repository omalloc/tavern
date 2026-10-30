package representation_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

func TestPrefixBarrierRejectsInFlightBeforeReserveAndCommit(t *testing.T) {
	for _, mode := range []representation.InvalidationMode{representation.InvalidateSoft, representation.InvalidateHard} {
		for _, reserved := range []bool{false, true} {
			t.Run(string(mode)+map[bool]string{false: "/before-reserve", true: "/before-commit"}[reserved], func(t *testing.T) {
				f := newFixture(t)
				ref, md := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"a"}}, "in-flight", "body", f.now.Unix())
				state, err := f.repo.Capture(t.Context(), f.key)
				if err != nil {
					t.Fatal(err)
				}
				var lease *representation.Reservation
				if reserved {
					lease, err = f.repo.ReserveFor(t.Context(), state, ref, 1)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := f.repo.InvalidatePrefix(t.Context(), "https://EXAMPLE.com/", mode); err != nil {
					t.Fatal(err)
				}
				if _, err := f.repo.ReserveFor(t.Context(), state, ref, 1); !errors.Is(err, representation.ErrSuperseded) {
					t.Fatal("ticket crossed barrier", err)
				}
				if reserved {
					called := false
					published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { called = true; return nil })
					if err != nil || published || called {
						t.Fatal("late commit touched child metadata", published, called, err)
					}
				}
				fresh, err := f.repo.Capture(t.Context(), f.key)
				if err != nil {
					t.Fatal(err)
				}
				other, _ := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {"b"}})
				lease, err = f.repo.ReserveFor(t.Context(), fresh, other, 1)
				if err != nil {
					t.Fatal("old epoch consumed pending budget", err)
				}
				f.repo.Release(lease)
			})
		}
	}
}

func TestPrefixBarrierSoftValidationAndHardCutoff(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"a"}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "original", "body", f.now.Unix())
	f.publish(t, ref)
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/", representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || selected.Fresh {
		t.Fatal("soft barrier left old bytes fresh", selected, err)
	}
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	md.Representation.ValidatedEpoch = state.Resource().Resource.RevalidateEpoch
	published, err := f.repo.Publish(t.Context(), state, lease, ref, md, &ref, func() error { return f.bucket.Store(t.Context(), md) })
	if err != nil || !published {
		t.Fatal(published, err)
	}
	selected, err = f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || !selected.Fresh {
		t.Fatal("validated bytes did not become fresh", selected, err)
	}
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	if selected, err := f.repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) || selected.Representation != nil {
		t.Fatal("hard barrier left a usable reference", selected, err)
	}
	file, _, err := f.bucket.ReadChunkFile(t.Context(), md.ID, 0)
	if err != nil {
		t.Fatal("logical invalidation deleted an active reader's bytes", err)
	}
	file.Close()
	state, err = f.repo.Capture(t.Context(), f.key)
	if err != nil || len(state.Resource().Resource.Variants) != 0 || state.Resource().Resource.PurgeEpoch != 1 {
		t.Fatal("new fill did not apply barrier", err)
	}
}

func TestPrefixBarrierCanonicalBoundariesAndColdResources(t *testing.T) {
	f := newFixture(t)
	type candidate struct {
		key      object.ResourceKey
		state    *representation.IndexState
		affected bool
	}
	var candidates []candidate
	for _, tc := range []struct {
		url      string
		affected bool
	}{
		{"https://EXAMPLE.com/img/a?x=1&x=2", true},
		{"https://example.com/img/sub/a", true},
		{"https://example.com/image/a", false},
		{"https://example.com/img%2Fa", false},
		{"http://example.com/img/a", false},
		{"https://other.example/img/a", false},
		{"https://example.com:444/img/a", false},
	} {
		key, _ := object.NewResourceKey(tc.url, "http-v2", "origin")
		state, err := f.repo.Capture(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidate{key, state, tc.affected})
	}
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/img/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	selector, _ := representation.NewHTTPSelector(nil, nil)
	for _, tc := range candidates {
		lease, err := f.repo.ReserveFor(t.Context(), tc.state, selector, 1)
		if tc.affected && !errors.Is(err, representation.ErrSuperseded) || !tc.affected && err != nil {
			t.Fatal(tc.key.CanonicalURL(), err)
		}
		f.repo.Release(lease)
	}
	// No root existed for this namespace/isolation scope when invalidated.
	cold, _ := object.NewResourceKey("https://example.com/img/cold", "another-namespace", "another-origin")
	state, err := f.repo.Capture(t.Context(), cold)
	if err != nil || state.Resource().Resource.PurgeEpoch != 1 || state.Resource().Resource.PrefixHardEpoch == 0 {
		t.Fatal("cold resource bypassed prefix barrier", err)
	}
	for _, invalid := range []string{"https://example.com/img", "https://example.com/img/?q=1", "https://example.com/img/#fragment"} {
		if err := f.repo.InvalidatePrefix(t.Context(), invalid, representation.InvalidateHard); !errors.Is(err, representation.ErrInvalidRecord) {
			t.Fatal("accepted ambiguous directory", invalid, err)
		}
	}
}

func TestInvalidationFailuresBudgetAndCatalogIdentity(t *testing.T) {
	f := newFixture(t)
	lookup := func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	}
	repo, err := representation.NewIndexRepository(f.controls, f.bucket.ID(), lookup, representation.WithBarrierLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InvalidatePrefix(t.Context(), "https://example.com/a/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	if err := repo.InvalidatePrefix(t.Context(), "https://example.com/b/", representation.InvalidateSoft); !errors.Is(err, representation.ErrBarrierLimit) {
		t.Fatal("budget exhaustion was acknowledged", err)
	}
	if err := repo.InvalidatePrefix(t.Context(), "https://example.com/a/", representation.InvalidateSoft); err != nil {
		t.Fatal("existing prefix consumed another entry", err)
	}
	failed, err := representation.NewIndexRepository(failingControls{f.controls}, f.bucket.ID(), lookup)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{
		func() error {
			return failed.InvalidatePrefix(t.Context(), "https://example.com/new/", representation.InvalidateHard)
		},
		func() error { return failed.Invalidate(t.Context(), f.key, representation.InvalidateHard) },
	} {
		if err := operation(); err == nil {
			t.Fatal("failed write acknowledged invalidation")
		}
	}
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate control-history loss/reconstruction without deleting protected
	// records through ordinary lifecycle APIs. Old roots cannot adopt it.
	catalog, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil {
		t.Fatal(err)
	}
	catalog.Barriers.Incarnation = "reconstructed-control-history"
	if err := f.controls.StoreControl(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	selector, _ := representation.NewHTTPSelector(nil, nil)
	if _, err := f.repo.ReserveFor(t.Context(), state, selector, 1); !errors.Is(err, representation.ErrSuperseded) {
		t.Fatal(err)
	}
	if _, err := f.repo.Capture(t.Context(), f.key); !errors.Is(err, representation.ErrInvalidRecord) {
		t.Fatal("old root adopted missing barrier history", err)
	}
}

func TestSingleResourceInvalidationCreatesColdIdentity(t *testing.T) {
	f := newFixture(t)
	cold, _ := object.NewResourceKey("https://example.com/cold", "http-v2", "origin")
	if err := f.repo.Invalidate(t.Context(), cold, representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.Capture(t.Context(), cold)
	if err != nil || state.Resource().Resource.PurgeEpoch != 1 {
		t.Fatal("cold invalidation had no identity", err)
	}
	if err := f.repo.Invalidate(t.Context(), cold, representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	selector, _ := representation.NewHTTPSelector(nil, nil)
	if _, err := f.repo.ReserveFor(t.Context(), state, selector, 1); !errors.Is(err, representation.ErrSuperseded) {
		t.Fatal("old ticket survived soft invalidation", err)
	}
}

func TestBarrierControlProtectedFromOrdinaryLifecycle(t *testing.T) {
	for _, backend := range []string{"memory", "disk"} {
		t.Run(backend, func(t *testing.T) {
			var f *fixture
			if backend == "memory" {
				f = newFixture(t)
			} else {
				f = diskFixture(t, "")
			}
			if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/img/", representation.InvalidateHard); err != nil {
				t.Fatal(err)
			}
			md, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
			if err != nil {
				t.Fatal(err)
			}
			if f.bucket.Objects() != 0 || f.bucket.Exist(t.Context(), md.ID.Bytes()) {
				t.Fatal("barrier entered ordinary object accounting")
			}
			for _, operation := range []func() error{
				func() error { return f.bucket.Store(t.Context(), md) },
				func() error { return f.bucket.Remove(t.Context(), md.ID) },
				func() error { return f.bucket.Discard(t.Context(), md.ID) },
				func() error { return f.bucket.DiscardWithHash(t.Context(), md.ID.Hash()) },
				func() error { return f.bucket.Migrate(t.Context(), md.ID, f.bucket) },
			} {
				if err := operation(); !errors.Is(err, storage.ErrProtectedControl) {
					t.Fatal("ordinary lifecycle modified barrier", err)
				}
			}
			if _, _, err := f.bucket.WriteChunkFile(t.Context(), md.ID, 0); !errors.Is(err, storage.ErrProtectedControl) {
				t.Fatal(err)
			}
			count := 0
			if err := f.bucket.Iterate(t.Context(), func(*object.Metadata) error { count++; return nil }); err != nil || count != 0 {
				t.Fatal("barrier entered ordinary scans", count, err)
			}
			if _, err := f.controls.LoadControl(t.Context(), md.ID); err != nil {
				t.Fatal("ordinary operations damaged catalog", err)
			}
		})
	}
}

type alteredCatalog struct {
	storage.ControlStore
	alter func(*object.BarrierCatalog)
}

func (c alteredCatalog) LoadControl(ctx context.Context, id *object.ID) (*object.Metadata, error) {
	md, err := c.ControlStore.LoadControl(ctx, id)
	if err == nil && md.Barriers != nil {
		c.alter(md.Barriers)
	}
	return md, err
}

func TestInvalidBarrierHistoryCannotBeOverwritten(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name  string
		alter func(*object.BarrierCatalog)
		want  error
	}{
		{"future-schema", func(c *object.BarrierCatalog) { c.SchemaVersion++ }, representation.ErrUnsupportedSchema},
		{"missing-identity", func(c *object.BarrierCatalog) { c.Incarnation = "" }, representation.ErrInvalidRecord},
		{"future-epoch", func(c *object.BarrierCatalog) {
			c.Entries = []object.PrefixBarrier{{CanonicalPrefix: "https://example.com/", HardEpoch: 1}}
		}, representation.ErrInvalidRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := representation.NewIndexRepository(alteredCatalog{f.controls, tc.alter}, f.bucket.ID(), func(context.Context, string, *object.ID) (*object.Metadata, error) {
				return nil, storage.ErrKeyNotFound
			})
			if !errors.Is(err, tc.want) {
				t.Fatal("invalid control history accepted/reset", err)
			}
			catalog, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
			if err != nil || catalog.Barriers.Revision != 0 || catalog.Barriers.Incarnation == "" {
				t.Fatal("constructor changed stored barrier history", catalog, err)
			}
		})
	}
}
