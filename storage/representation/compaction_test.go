package representation_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/storage/representation"
)

func TestBarrierCompactionSyncFailureAndDiskRestart(t *testing.T) {
	base := filepath.Join(t.TempDir(), "data")
	f := diskFixture(t, base)
	if err := f.repo.InvalidateURL(t.Context(), f.key.CanonicalURL(), representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/img/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	inc, hard, soft := observedBarrierEpochs(t, f, f.key.CanonicalURL())
	controls := &probedControls{DurableControlStore: f.controls.(storage.DurableControlStore), events: &durableEvents{}}
	f.repo = durableRepo(t, controls, f.bucket)
	controls.fail.Store(true)
	if result, err := f.repo.CompactBarriers(t.Context(), 128); err == nil || result.Removed != 0 {
		t.Fatal("failed durable compaction acknowledged removals", result, err)
	}
	md, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil || len(md.Barriers.Entries) != 2 {
		t.Fatal("failed compaction dropped persisted history", err)
	}
	controls.fail.Store(false)
	if result, err := f.repo.CompactBarriers(t.Context(), 128); err != nil || result.Removed != 1 {
		t.Fatal(result, err)
	}
	if err := f.bucket.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := diskFixture(t, base)
	gotInc, gotHard, gotSoft := observedBarrierEpochs(t, reopened, f.key.CanonicalURL())
	if gotInc != inc || gotHard != hard || gotSoft != soft {
		t.Fatal("restart changed compacted comparisons", gotInc, gotHard, gotSoft)
	}
	md, err = reopened.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil || len(md.Barriers.Entries) != 1 {
		t.Fatal("compaction was not durable", err)
	}
}

func observedBarrierEpochs(t *testing.T, f *fixture, raw string) (string, uint64, uint64) {
	t.Helper()
	md, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	hard, soft := uint64(0), uint64(0)
	for _, entry := range md.Barriers.Entries {
		prefix, err := url.Parse(entry.CanonicalPrefix)
		if err != nil {
			t.Fatal(err)
		}
		matches := entry.Exact && raw == entry.CanonicalPrefix || !entry.Exact && target.Scheme == prefix.Scheme && target.Host == prefix.Host && strings.HasPrefix(target.EscapedPath(), prefix.EscapedPath())
		if matches {
			hard = max(hard, entry.HardEpoch)
			soft = max(soft, entry.SoftEpoch)
		}
	}
	return md.Barriers.Incarnation, hard, soft
}
func TestBarrierCompactionPreservesEveryMatchingEpoch(t *testing.T) {
	f := newFixture(t)
	for _, event := range []struct {
		raw   string
		mode  representation.InvalidationMode
		exact bool
	}{
		{"https://example.com/img/a?x=1", representation.InvalidateHard, true},
		{"https://example.com/img/a?x=1", representation.InvalidateSoft, true},
		{"https://example.com/img/sub/", representation.InvalidateHard, false},
		{"https://example.com/img/sub/", representation.InvalidateSoft, false},
		{"https://example.com/img%2Fa", representation.InvalidateHard, true},
		{"https://other.example/img/a", representation.InvalidateSoft, true},
		{"https://example.com/img/", representation.InvalidateHard, false},
		{"https://example.com/img/", representation.InvalidateSoft, false},
	} {
		var err error
		if event.exact {
			err = f.repo.InvalidateURL(t.Context(), event.raw, event.mode)
		} else {
			err = f.repo.InvalidatePrefix(t.Context(), event.raw, event.mode)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	urls := []string{"https://example.com/img/a?x=1", "https://example.com/img/a?x=2", "https://example.com/img/", "https://example.com/img/?x=1", "https://example.com/img/sub/new", "https://example.com/img%2Fa", "https://example.com/img2/a", "https://other.example/img/a", "http://example.com/img/a"}
	before := make(map[string]string)
	for _, raw := range urls {
		inc, hard, soft := observedBarrierEpochs(t, f, raw)
		before[raw] = fmt.Sprint(inc, "/", hard, "/", soft)
	}
	removed := 0
	for i := 0; i < 10; i++ {
		result, err := f.repo.CompactBarriers(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if result.Scanned > 1 {
			t.Fatal("compaction exceeded candidate budget")
		}
		removed += result.Removed
	}
	if removed != 2 {
		t.Fatalf("expected only covered exact and subdirectory records, removed=%d", removed)
	}
	for _, raw := range urls {
		inc, hard, soft := observedBarrierEpochs(t, f, raw)
		if got := fmt.Sprint(inc, "/", hard, "/", soft); got != before[raw] {
			t.Fatalf("changed epochs for %s: before=%s after=%s", raw, before[raw], got)
		}
	}
	// A newer hard barrier alone must not drop an older soft comparison.
	if err := f.repo.InvalidateURL(t.Context(), "https://example.com/independent/a", representation.InvalidateSoft); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.InvalidatePrefix(t.Context(), "https://example.com/independent/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	beforeCatalog, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.repo.CompactBarriers(t.Context(), 128)
	if err != nil {
		t.Fatal(err)
	}
	afterCatalog, err := f.controls.LoadControl(t.Context(), object.BarrierCatalogID())
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 || len(afterCatalog.Barriers.Entries) != len(beforeCatalog.Barriers.Entries) {
		t.Fatal("hard-only cover discarded distinct soft history")
	}
}
func TestBarrierCompactionPreservesFreshRootAndInFlightTicket(t *testing.T) {
	f := newFixture(t)
	oldState, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	oldRef, oldMD := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"old"}}, "old", "old", f.now.Unix())
	oldLease, err := f.repo.ReserveFor(t.Context(), oldState, oldRef, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.InvalidateURL(t.Context(), f.key.CanonicalURL(), representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.InvalidatePrefix(t.Context(), "https://example.com/", representation.InvalidateHard); err != nil {
		t.Fatal(err)
	}
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	freshRef, freshMD := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"fresh"}}, "fresh", "fresh", f.now.Unix())
	freshLease, err := f.repo.ReserveFor(t.Context(), state, freshRef, 10)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.repo.CompactBarriers(t.Context(), 128)
	if err != nil || result.Removed != 1 {
		t.Fatal(result, err)
	}
	if ok, err := f.repo.Publish(t.Context(), state, freshLease, freshRef, freshMD, nil, func() error { return f.bucket.Store(t.Context(), freshMD) }); !ok || err != nil {
		t.Fatal("compaction invalidated current ticket", ok, err)
	}
	if selected, err := f.repo.Lookup(t.Context(), f.key, http.Header{"Accept": {"fresh"}}); err != nil || !selected.Fresh {
		t.Fatal("compaction changed fresh reachability", err)
	}
	called := false
	if ok, err := f.repo.Publish(t.Context(), oldState, oldLease, oldRef, oldMD, nil, func() error { called = true; return nil }); ok || err != nil || called {
		t.Fatal("compaction revived old fill", ok, err)
	}
	// Public API honors cancellation and never uses wall-clock age as authority.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = f.repo.CompactBarriers(ctx, 1); err == nil {
		t.Fatal("cancelled compaction accepted")
	}
}
