package caching

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func representationMetricSample(t *testing.T, name string, labels map[string]string) float64 {
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
			actual := map[string]string{}
			for _, label := range metric.Label {
				actual[label.GetName()] = label.GetValue()
			}
			matched := true
			for key, value := range labels {
				if actual[key] != value {
					matched = false
				}
			}
			if matched {
				sum += metric.GetGauge().GetValue() + metric.GetCounter().GetValue()
				if metric.Histogram != nil {
					sum += float64(metric.Histogram.GetSampleCount())
				}
			}
		}
	}
	return sum
}
func TestManagedMetricsCountConversionsNotProductHits(t *testing.T) {
	f := newManagedFixture(t)
	sample := func(name string, labels map[string]string) float64 {
		return representationMetricSample(t, "tr_tavern_"+name, labels)
	}
	beforeSuccess := sample("transform_total", map[string]string{"backend": "other", "result": "success"})
	beforeFailed := sample("transform_total", map[string]string{"backend": "other", "result": "failed"})
	beforeSourceMiss := sample("source_fetch_total", map[string]string{"result": "miss"})
	beforeSourceHit := sample("source_fetch_total", map[string]string{"result": "hit"})
	beforeOrigin := sample("representation_pending", map[string]string{"kind": "origin"})
	beforeDerived := sample("representation_pending", map[string]string{"kind": "derived"})
	f.get(t, "format=png", "image/png")
	f.get(t, "format=png", "image/png")
	if sample("transform_total", map[string]string{"backend": "other", "result": "success"}) != beforeSuccess+1 {
		t.Fatal("product hit counted as new conversion")
	}
	f.backend.failure.Store(true)
	req, plan := f.request(t, "format=jpeg&quality=70", "image/jpeg")
	if _, err := f.provider.resolve(req, plan); err == nil {
		t.Fatal("expected injected conversion error")
	}
	if sample("transform_total", map[string]string{"backend": "other", "result": "failed"}) != beforeFailed+1 {
		t.Fatal("conversion failure not counted")
	}
	f.backend.failure.Store(false)
	f.get(t, "format=jpeg&quality=70", "image/jpeg")
	if sample("source_fetch_total", map[string]string{"result": "miss"}) != beforeSourceMiss+1 || sample("source_fetch_total", map[string]string{"result": "hit"}) != beforeSourceHit+3 {
		t.Fatal("source reuse metrics mismatch")
	}
	if sample("representation_pending", map[string]string{"kind": "origin"}) != beforeOrigin || sample("representation_pending", map[string]string{"kind": "derived"}) != beforeDerived {
		t.Fatal("completed/failing conversions leaked pending gauges")
	}
	if sample("transform_duration_seconds", map[string]string{"backend": "other", "result": "failed"}) < 1 {
		t.Fatal("failed conversion duration not observed")
	}
}
func TestVaryMetricsSeparateWildcardAdmissionFromLookup(t *testing.T) {
	f := newOriginFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "*")
		w.Header().Set("Cache-Control", "max-age=600")
		w.Write([]byte("private selection"))
	})
	labels := map[string]string{"result": "bypass", "reason": "wildcard"}
	before := representationMetricSample(t, "tr_tavern_vary_admission_total", labels)
	if _, _, err := readVaryResponse(f.rt, varyRequest(t, f.target, nil)); err != nil {
		t.Fatal(err)
	}
	if representationMetricSample(t, "tr_tavern_vary_admission_total", labels) != before+1 {
		t.Fatal("wildcard admission event missing or double counted")
	}
}
