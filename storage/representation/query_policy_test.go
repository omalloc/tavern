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

func TestQuerylessPurgePreservesQueryAwareNamespaces(t *testing.T) {
	for _, hard := range []bool{false, true} {
		f := newFixture(t)
		if err := f.repo.BindQuerylessNamespace(t.Context(), "queryless"); err != nil {
			t.Fatal(err)
		}
		selector, _ := representation.NewHTTPSelector(nil, nil)
		for _, tc := range []struct {
			namespace, raw string
			invalid        bool
		}{
			{"queryless", "https://example.com/image", true},
			{"http-v2", "https://example.com/image?x=1", true},
			{"http-v2", "https://example.com/image", false},
			{"http-v2", "https://example.com/image?x=2", false},
		} {
			key, _ := object.NewResourceKey(tc.raw, tc.namespace, "origin")
			state, err := f.repo.Capture(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.repo.Purge(t.Context(), "https://example.com/image?x=1", storage.PurgeControl{Hard: hard}); err != nil {
				t.Fatal(err)
			}
			lease, err := f.repo.ReserveFor(t.Context(), state, selector, 1)
			if tc.invalid && !errors.Is(err, representation.ErrSuperseded) {
				t.Fatal("URI mapping did not fence the prior ticket", tc, err)
			}
			if !tc.invalid && err != nil {
				t.Fatal("query-aware resource was over-invalidated", tc, err)
			}
			f.repo.Release(lease)
		}
	}
}

func TestQuerylessBindingRetainsPriorPurgeHistoryAndReopens(t *testing.T) {
	f := newFixture(t)
	key, _ := object.NewResourceKey("https://example.com/image", "queryless", "origin")
	state, err := f.repo.Capture(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Purge(t.Context(), key.CanonicalURL()+"?x=1", storage.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.BindQuerylessNamespace(t.Context(), key.Namespace()); err != nil {
		t.Fatal(err)
	}
	reopened, err := representation.NewIndexRepository(f.controls, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	selector, _ := representation.NewHTTPSelector(nil, http.Header{})
	if lease, err := reopened.ReserveFor(t.Context(), state, selector, 1); !errors.Is(err, representation.ErrSuperseded) {
		reopened.Release(lease)
		t.Fatal("reopened mapping lost prior URI purge", err)
	}
}

func TestQuerylessPurgeBudgetFailureDoesNotPartiallyCommit(t *testing.T) {
	f := newFixture(t)
	repo, err := representation.NewIndexRepository(f.controls, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	}, representation.WithBarrierLimit(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.BindQuerylessNamespace(t.Context(), "queryless"); err != nil {
		t.Fatal(err)
	}
	before, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Purge(t.Context(), "https://example.com/image?x=1", storage.PurgeControl{Hard: true}); !errors.Is(err, representation.ErrBarrierLimit) {
		t.Fatal(err)
	}
	after, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil {
		t.Fatal(err)
	}
	if after.Barriers.Revision != before.Barriers.Revision || len(after.Barriers.Entries) != 0 {
		t.Fatal("failed compound purge partially committed")
	}
}
