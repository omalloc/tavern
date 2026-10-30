package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRepresentationMetricLabelsCannotContainRequestValues(t *testing.T) {
	for i := 0; i < 100; i++ {
		secret := fmt.Sprintf("https://private.example/a?cookie=secret-%d", i)
		RecordVaryLookup(secret, secret)
		RecordVaryAdmission(secret, secret)
		RecordTransform(secret, secret, secret, secret, time.Millisecond)
		RepresentationPending(secret, 1)
		RepresentationPending(secret, -1)
		TransformBudget(secret, secret, 1, 1)
		TransformBudget(secret, secret, -1, -1)
		RecordTransformRejected(secret, secret, secret)
		RecordSourceFetch(secret)
		RecordRepresentationGC(secret)
	}
	families, err := tavernRegistry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), Namespace+"_") {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if strings.Contains(label.GetValue(), "private.example") || strings.Contains(label.GetValue(), "secret-") {
					t.Fatalf("sensitive/high-cardinality label: %s %v", family.GetName(), label)
				}
			}
		}
		if family.GetName() == Namespace+"_transform_total" {
			for _, metric := range family.Metric {
				if len(metric.Label) == 4 && metric.GetCounter().GetValue() == 100 {
					found++
				}
			}
		}
	}
	if found != 1 {
		t.Fatal("unexpected dynamic transform series", found)
	}
}
