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

func TestRetireUsesStoredIDAndProtectsNewerReference(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {`image/webp;profile="a,b", image/jpeg;q=0.8`}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "generation", "body", f.now.Unix())
	f.publish(t, ref)
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	updated := md.Clone()
	updated.Headers.Set("Cache-Control", "max-age=100")
	published, err := f.repo.Publish(t.Context(), state, lease, ref, updated, &ref, func() error { return f.bucket.Store(t.Context(), updated) })
	if err != nil || !published {
		t.Fatal(published, err)
	}
	if retired, err := f.repo.Retire(t.Context(), f.key, ref); !errors.Is(err, representation.ErrSuperseded) || retired != nil {
		t.Fatal("old observation retired newer validation", retired, err)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := f.repo.Retire(t.Context(), f.key, selected.Reference)
	if err != nil || retired.ID.Hash() != md.ID.Hash() || retired.Location != f.bucket.ID() {
		t.Fatal("retirement reconstructed a selector key", retired, err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("retired reference is still readable", err)
	}
	// Logical retirement keeps the body available for readers already using it.
	if file, _, err := f.bucket.ReadChunkFile(t.Context(), md.ID, 0); err != nil {
		t.Fatal(err)
	} else {
		file.Close()
	}
}

func TestRetireControlFailureDoesNotAuthorizeCleanup(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg,image/webp;q=0.8"}}
	ref, _ := f.candidate(t, []string{"Accept"}, header, "generation", "body", f.now.Unix())
	f.publish(t, ref)
	repo, err := representation.NewIndexRepository(failingControls{f.controls}, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if retired, err := repo.Retire(t.Context(), f.key, ref); err == nil || retired != nil {
		t.Fatal("failed control write authorized physical cleanup", retired, err)
	}
	if _, body := f.body(t, header); body != "body" {
		t.Fatal("failed retirement damaged a published representation", body)
	}
}
