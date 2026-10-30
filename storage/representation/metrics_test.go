package representation_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/prometheus/client_golang/prometheus"
)

func representationObserved(t *testing.T, name, label, value string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	sum := float64(0)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			matched := false
			for _, pair := range metric.Label {
				if pair.GetName() == label && pair.GetValue() == value {
					matched = true
				}
			}
			if matched {
				sum += metric.GetGauge().GetValue() + metric.GetCounter().GetValue()
			}
		}
	}
	return sum
}
func TestPublicationMetricsReleaseFailedAndSupersededLeases(t *testing.T) {
	f := newFixture(t)
	sample := func(name, label, value string) float64 {
		return representationObserved(t, "tr_tavern_"+name, label, value)
	}
	pending := sample("representation_pending", "kind", "origin")
	failed := sample("representation_commit_total", "result", "failed")
	published := sample("representation_commit_total", "result", "published")
	superseded := sample("representation_commit_total", "result", "superseded")
	ref, md := f.candidate(t, []string{"Accept"}, http.Header{"Accept": {"image/jpeg"}}, "metrics-1", "bytes", f.now.Unix())
	state, err := f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sample("representation_pending", "kind", "origin") != pending+1 {
		t.Fatal("pending lease not observable")
	}
	injected := errors.New("injected child persistence failure")
	if ok, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return injected }); ok || !errors.Is(err, injected) {
		t.Fatal(ok, err)
	}
	f.repo.Release(lease)
	if sample("representation_pending", "kind", "origin") != pending || sample("representation_commit_total", "result", "failed") != failed+1 {
		t.Fatal("failed publication leaked or was not counted")
	}
	lease, err = f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { return f.bucket.Store(t.Context(), md) }); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if sample("representation_commit_total", "result", "published") != published+1 {
		t.Fatal("published root not counted")
	}
	state, err = f.repo.Capture(t.Context(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = f.repo.ReserveFor(t.Context(), state, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.Purge(t.Context(), f.key.CanonicalURL(), storage.PurgeControl{Hard: true}); err != nil {
		t.Fatal(err)
	}
	stored := false
	if ok, err := f.repo.Publish(t.Context(), state, lease, ref, md, nil, func() error { stored = true; return nil }); ok || err != nil || stored {
		t.Fatal("late publish was not fenced", ok, err, stored)
	}
	if sample("representation_pending", "kind", "origin") != pending || sample("representation_commit_total", "result", "superseded") != superseded+1 {
		t.Fatal("superseded lease did not settle metrics")
	}
}
