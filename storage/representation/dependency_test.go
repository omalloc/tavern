package representation_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

func sourceFixture(t *testing.T) (*fixture, *representation.LookupResult) {
	t.Helper()
	f := newFixture(t)
	header := http.Header{"Accept": {"image/jpeg"}}
	ref, md := f.candidate(t, []string{"Accept"}, header, "source-a", "origin", f.now.Unix())
	md.Headers.Set("Cache-Control", "max-age=3600")
	if err := f.bucket.Store(t.Context(), md); err != nil {
		t.Fatal(err)
	}
	f.publish(t, ref)
	source, err := f.repo.Lookup(t.Context(), f.key, header)
	if err != nil {
		t.Fatal(err)
	}
	return f, source
}

func derivedCandidate(t *testing.T, f *fixture, source *representation.LookupResult, spec string) (object.VariantRef, *object.Metadata) {
	t.Helper()
	fingerprint := object.TransformFingerprint([]byte(spec), "policy-1", "encoder-1")
	ref, err := representation.NewManagedSelector(source, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.key.RepresentationID(root.Resource.Incarnation, root.Resource.PurgeEpoch, ref.SelectorFingerprint, "output-"+fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	ref.RepresentationID, ref.ResponseDate = id, f.now.Unix()
	md := source.Representation.Clone()
	md.ID = id
	md.Size = 4
	md.ExpiresAt = f.now.Unix() + 500
	md.Headers = source.Representation.Headers.Clone()
	md.Headers.Set("Content-Length", "4")
	md.Headers.Set("Content-Type", "image/webp")
	md.Headers.Set("Vary", "Accept")
	md.Headers.Set("Cache-Control", "max-age=500")
	info := md.Representation
	info.Revision = 0
	info.Kind = object.RepresentationDerived
	info.BodyGeneration = "output-" + fingerprint
	info.StorageSelectorFingerprint = ref.SelectorFingerprint
	info.SourceID, info.SourceVersion = source.Representation.ID.Clone(), source.Representation.Representation.BodyGeneration
	info.TransformSpec, info.PolicyVersion, info.EncoderVersion = []byte(spec), "policy-1", "encoder-1"
	writer, _, err := f.bucket.WriteChunkFile(t.Context(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "webp"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return ref, md
}

func publishDerived(t *testing.T, f *fixture, ref object.VariantRef, md *object.Metadata, limit int) {
	t.Helper()
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, limit)
	if err != nil {
		t.Fatal(err)
	}
	published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return f.bucket.Store(t.Context(), md) })
	if err != nil || !published {
		t.Fatal("derived publish", published, err)
	}
}

func TestDerivedDependencyRetirementAndSourceValidation(t *testing.T) {
	f, source := sourceFixture(t)
	refs := []object.VariantRef{}
	for _, quality := range []int{80, 90} {
		ref, md := derivedCandidate(t, f, source, fmt.Sprintf(`{"mime":"image/webp","quality":%d}`, quality))
		publishDerived(t, f, ref, md, 2)
		refs = append(refs, ref)
	}
	selected, err := f.repo.LookupDerived(t.Context(), f.key, source.Representation.ID, source.Representation.Representation.BodyGeneration, refs[0].TransformFingerprint)
	if err != nil || !selected.Fresh {
		t.Fatal(selected, err)
	}
	if _, err := f.repo.Retire(t.Context(), f.key, selected.Reference); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.LookupDerived(t.Context(), f.key, source.Representation.ID, source.Representation.Representation.BodyGeneration, refs[1].TransformFingerprint); err != nil {
		t.Fatal("one product retirement damaged another", err)
	}
	if _, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"image/jpeg"}}); err != nil {
		t.Fatal("product retirement damaged origin", err)
	}
	// A successful 304 keeps body identity/version despite its paired metadata revision changing.
	state, _ := f.repo.Capture(t.Context(), f.key)
	lease, err := f.repo.ReserveFor(t.Context(), state, source.Reference, 1)
	if err != nil {
		t.Fatal(err)
	}
	md := source.Representation.Clone()
	md.ExpiresAt += 100
	published, err := f.repo.Publish(t.Context(), state, lease, source.Reference, md, &source.Reference, func() error { return f.bucket.Store(t.Context(), md) })
	if err != nil || !published {
		t.Fatal(published, err)
	}
	if _, err := f.repo.LookupDerived(t.Context(), f.key, source.Representation.ID, source.Representation.Representation.BodyGeneration, refs[1].TransformFingerprint); err != nil {
		t.Fatal("304 broke unchanged content dependency", err)
	}
	if err := f.repo.Invalidate(t.Context(), f.key, representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.LookupDerived(t.Context(), f.key, source.Representation.ID, source.Representation.Representation.BodyGeneration, refs[1].TransformFingerprint); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("output TTL bypassed source invalidation", err)
	}
}

func TestSourceReplacementRejectsLateDerivedCommit(t *testing.T) {
	f, source := sourceFixture(t)
	ref, md := derivedCandidate(t, f, source, `{"mime":"image/webp","quality":80}`)
	publishDerived(t, f, ref, md, 2)
	lateRef, lateMD := derivedCandidate(t, f, source, `{"mime":"image/webp","quality":90}`)
	oldState, _ := f.repo.Capture(t.Context(), f.key)
	oldLease, err := f.repo.ReserveFor(t.Context(), oldState, lateRef, 2)
	if err != nil {
		t.Fatal(err)
	}
	newRef, newMD := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"image/jpeg"}}, "source-b", "new-origin", f.now.Unix()+1)
	newMD.Headers.Set("Cache-Control", "max-age=3600")
	state, _ := f.repo.Capture(t.Context(), f.key)
	lease, err := f.repo.ReserveFor(t.Context(), state, newRef, 1)
	if err != nil {
		t.Fatal(err)
	}
	published, err := f.repo.Publish(t.Context(), state, lease, newRef, newMD, &source.Reference, func() error { return f.bucket.Store(t.Context(), newMD) })
	if err != nil || !published {
		t.Fatal(published, err)
	}
	called := false
	published, err = f.repo.Publish(t.Context(), oldState, oldLease, lateRef, lateMD, nil, func() error { called = true; return nil })
	if err != nil || published || called {
		t.Fatal("source changed during transform", published, called, err)
	}
	if _, err := f.repo.LookupDerived(t.Context(), f.key, source.Representation.ID, source.Representation.Representation.BodyGeneration, ref.TransformFingerprint); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("old product remained reachable", err)
	}
	root, err := f.repo.Load(t.Context(), f.key)
	if err != nil || len(root.Resource.Variants) != 1 {
		t.Fatal("obsolete dependency references retained", err)
	}
	file, _, err := f.bucket.ReadChunkFile(t.Context(), md.ID, 0)
	if err != nil {
		t.Fatal("logical dependency retirement deleted old reader bytes", err)
	}
	file.Close()
}

func TestDerivedAdmissionRequiresSourcePolicyAndExpiry(t *testing.T) {
	for _, kind := range []string{"source-private", "source-expired", "derived-private", "too-long-output-ttl", "wrong-source-version"} {
		t.Run(kind, func(t *testing.T) {
			f, source := sourceFixture(t)
			ref, md := derivedCandidate(t, f, source, `{"mime":"image/webp"}`)
			if kind == "source-private" || kind == "source-expired" {
				copy := source.Representation.Clone()
				if kind == "source-private" {
					copy.Headers.Set("Cache-Control", "max-age=3600,private")
				} else {
					copy.ExpiresAt = f.now.Unix() - 1
				}
				if err := f.bucket.Store(t.Context(), copy); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "derived-private" {
				md.Headers.Set("Cache-Control", "max-age=500,private")
			}
			if kind == "too-long-output-ttl" {
				md.ExpiresAt = source.Representation.ExpiresAt + 1
			}
			if kind == "wrong-source-version" {
				ref.SelectedSourceVersion = "wrong"
				ref.SelectorFingerprint, _ = object.ManagedSelectorFingerprint(ref.SelectedSourceID, ref.SelectedSourceVersion, ref.TransformFingerprint)
			}
			state, err := f.repo.Capture(t.Context(), f.key)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
			if kind == "source-private" || kind == "source-expired" || kind == "wrong-source-version" {
				if !errors.Is(err, representation.ErrSuperseded) {
					t.Fatal("invalid source admitted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			published, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { t.Error("ineligible product metadata written"); return nil })
			if err == nil || published {
				t.Fatal("ineligible product admitted", published, err)
			}
		})
	}
}

func TestSourceBoundDerivedLifetimeFollowsOnlySameFreshSourceVersion(t *testing.T) {
	f, source := sourceFixture(t)
	repo, err := representation.NewIndexRepository(f.controls, f.bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
		return f.bucket.Lookup(ctx, id)
	}, representation.WithClock(func() time.Time { return f.now }))
	if err != nil {
		t.Fatal(err)
	}
	f.repo = repo
	boundRef, bound := derivedCandidate(t, f, source, `{"quality":80}`)
	bound.Representation.SourceBoundTTL = true
	bound.ExpiresAt = source.Representation.ExpiresAt
	publishDerived(t, f, boundRef, bound, 2)
	ownRef, own := derivedCandidate(t, f, source, `{"quality":90}`)
	publishDerived(t, f, ownRef, own, 2)
	f.now = f.now.Add(2 * time.Hour)
	if _, err = f.repo.LookupDerived(t.Context(), f.key, source.Representation.ID, source.Representation.Representation.BodyGeneration, boundRef.TransformFingerprint); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("old product bypassed expired source", err)
	}
	if err = f.repo.Invalidate(t.Context(), f.key, representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	next := source.Representation.Clone()
	next.ExpiresAt = f.now.Add(time.Hour).Unix()
	next.RespUnix = f.now.Unix()
	next.Headers.Set("Date", f.now.UTC().Format(http.TimeFormat))
	next.Headers.Set("Cache-Control", "max-age=3600")
	next.Representation.ValidatedEpoch = state.Resource().Resource.RevalidateEpoch
	ref := source.Reference.Clone()
	ref.ResponseDate = f.now.Unix()
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	published, err := f.repo.Publish(t.Context(), state, lease, ref, next, &source.Reference, func() error { return f.bucket.Store(t.Context(), next) })
	if err != nil || !published {
		t.Fatal(published, err)
	}
	selected, err := f.repo.LookupDerived(t.Context(), f.key, next.ID, next.Representation.BodyGeneration, boundRef.TransformFingerprint)
	if err != nil || !selected.Fresh || selected.Representation.ExpiresAt != next.ExpiresAt || selected.Representation.ID.Hash() != bound.ID.Hash() {
		t.Fatal("same source validation did not retain bound product", selected, err)
	}
	if _, err = f.repo.LookupDerived(t.Context(), f.key, next.ID, next.Representation.BodyGeneration, ownRef.TransformFingerprint); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("own TTL was extended", err)
	}
	if err = f.repo.Invalidate(t.Context(), f.key, representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.LookupDerived(t.Context(), f.key, next.ID, next.Representation.BodyGeneration, boundRef.TransformFingerprint); !errors.Is(err, storage.ErrKeyNotFound) {
		t.Fatal("source-bound TTL bypassed hard invalidation", err)
	}
}
