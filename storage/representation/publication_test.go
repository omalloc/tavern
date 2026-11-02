package representation_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

func TestPublishConcurrentSelectors(t *testing.T) {
	f := newFixture(t)
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	const count = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range count {
		ref, md := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {fmt.Sprint(i)}}, fmt.Sprint(i), fmt.Sprint(i), f.now.Unix())
		lease, err := f.repo.ReserveFor(t.Context(), state, ref, count)
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			<-start
			published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return f.bucket.Store(t.Context(), md) })
			if err != nil || !published {
				t.Errorf("publish: %t, %v", published, err)
			}
		})
	}
	close(start)
	wg.Wait()
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != count {
		t.Fatal("lost concurrent reference", err)
	}
	for i := range count {
		if _, body := f.body(t, http.Header{"Accept": {fmt.Sprint(i)}}); body != fmt.Sprint(i) {
			t.Fatal(body)
		}
	}
}

func TestReservationReplacementKeepsOldReferenceAndBoundsPendingChoices(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"jpeg"}, "X-Theme": {"dark"}}
	old, _ := f.candidate(t, []string{"Accept"}, header, "old", "old body", f.now.Unix())
	f.publish(t, old)
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lateRef, lateMD := f.candidate(t, []string{"Accept"}, header, "late-old-selector", "late body", f.now.Unix()+2)
	lateLease, err := f.repo.ReserveFor(t.Context(), state, lateRef, 1)
	if err != nil {
		t.Fatal(err)
	}
	next, md := f.candidate(t, []string{"X-Theme"}, header, "next", "new body", f.now.Unix()+1)
	lease, err := f.repo.ReserveReplacementFor(t.Context(), state, next, &old, 1)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := representation.NewHTTPSelector([]string{"X-Other"}, http.Header{"X-Other": {"value"}})
	if pending, err := f.repo.ReserveReplacementFor(t.Context(), state, other, &old, 1); !errors.Is(err, representation.ErrSelectorLimit) {
		f.repo.Release(pending)
		t.Fatal("different pending replacements spent the same slot", err)
	}
	if _, body := f.body(t, header); body != "old body" {
		t.Fatal("reservation removed readable old body")
	}
	f.repo.Release(lease)
	if _, body := f.body(t, header); body != "old body" {
		t.Fatal("aborted replacement removed old reference")
	}
	lease, err = f.repo.ReserveReplacementFor(t.Context(), state, next, &old, 1)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if ok, err := f.repo.Publish(t.Context(), state, lease, next, md, nil, func() error { called = true; return nil }); ok || !errors.Is(err, representation.ErrInvalidRecord) || called {
		t.Fatal("borrowed slot published without retiring its owner", ok, err)
	}
	lease, err = f.repo.ReserveReplacementFor(t.Context(), state, next, &old, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.repo.Publish(t.Context(), state, lease, next, md, &old, func() error { return f.bucket.Store(t.Context(), md) }); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, body := f.body(t, header); body != "new body" {
		t.Fatal("replacement did not publish", body)
	}
	called = false
	if ok, err := f.repo.Publish(t.Context(), state, lateLease, lateRef, lateMD, nil, func() error { called = true; return f.bucket.Store(t.Context(), lateMD) }); ok || !errors.Is(err, representation.ErrSelectorLimit) || called {
		t.Fatal("late original selector exceeded the replacement's budget", ok, err)
	}
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != 1 {
		t.Fatal("replacement exceeded choice budget", err)
	}
}

func TestPublishLateValidationSameDate(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg"}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "a", "body", f.now.Unix())
	f.publish(t, ref)
	state, _ := f.repo.Capture(t.Context(), f.key)
	one, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, late := md.Clone(), md.Clone()
	first.Headers.Set("Cache-Control", "max-age=100")
	late.Headers.Set("Cache-Control", "max-age=1000")
	published, err := f.repo.Publish(t.Context(), state, one, ref, first, &ref, func() error { return f.bucket.Store(t.Context(), first) })
	if err != nil || !published {
		t.Fatal(published, err)
	}
	called := false
	published, err = f.repo.Publish(t.Context(), state, two, ref, late, &ref, func() error { called = true; return f.bucket.Store(t.Context(), late) })
	if err != nil || published || called {
		t.Fatal("late validation overwrote metadata with equal Date", published, called, err)
	}
	selected, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil || selected.Representation.Headers.Get("Cache-Control") != "max-age=100" {
		t.Fatal("lost newer metadata", err)
	}
}

func TestPublishControlFailureCannotRefreshOldReference(t *testing.T) {
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg"}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "a", "body", f.now.Unix())
	md.ExpiresAt = f.now.Unix() - 1
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	f.publish(t, ref)
	repo, err := representation.NewIndexRepository(failingControls{f.controls}, f.bucket.ID(), func(ctx context.Context, _ string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	fresh := md.Clone()
	fresh.ExpiresAt = f.now.Unix() + 1000 // Date and ID stay exactly the same.
	published, err := repo.Publish(t.Context(), state, lease, ref, fresh, &ref, func() error { return f.bucket.Store(t.Context(), fresh) })
	if err == nil || published {
		t.Fatal("failed root write confirmed publication")
	}
	if selected, err := f.repo.Lookup(t.Context(), f.key, header); !errors.Is(err, storage.ErrKeyNotFound) || selected.Representation != nil {
		t.Fatal("old reference acquired uncommitted freshness", selected, err)
	}
	other, _ := representation.NewHTTPSelector([]string{"Accept"}, http.Header{"Accept": {"other"}})
	lease, err = repo.ReserveFor(t.Context(), state, other, 2)
	if err != nil {
		t.Fatal("failed commit leaked reservation", err)
	}
	repo.Release(lease)
}

func TestPublishCapturedInvalidationRejectsLateResult(t *testing.T) {
	f := newFixture(t)
	ref, md := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"image/jpeg"}}, "a", "body", f.now.Unix())
	state, _ := f.repo.Capture(t.Context(), f.key)
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Update(t.Context(), f.key, func(root *object.ResourceRecord) error { root.RevalidateEpoch++; return nil }); err != nil {
		t.Fatal(err)
	}
	called := false
	published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { called = true; return nil })
	if err != nil || published || called {
		t.Fatal("late result crossed epoch", published, called, err)
	}
	if _, err := f.repo.ReserveFor(t.Context(), state, ref, 1); !errors.Is(err, representation.ErrSuperseded) {
		t.Fatal("reservation captured epoch after origin", err)
	}
}
