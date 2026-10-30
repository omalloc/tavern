package transform

import (
	"context"
	"errors"
	"github.com/prometheus/client_golang/prometheus"
	"runtime"
	"testing"
	"time"
)

func TestWorkBudgetBoundsAndCancellation(t *testing.T) {
	budget, err := NewWorkBudget(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	release, err := budget.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		r, err := budget.Acquire(ctx)
		if r != nil {
			r()
		}
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(budget.queued) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter did not enter queue")
		}
		runtime.Gosched()
	}
	if r, err := budget.Acquire(context.Background()); !errors.Is(err, ErrBusy) {
		if r != nil {
			r()
		}
		t.Fatalf("queue bound: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	release()
	if len(budget.active) != 0 || len(budget.queued) != 0 {
		t.Fatal("budget leaked")
	}
	r, err := budget.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r()
}

func transformBudgetSample(t *testing.T, name string, labels map[string]string) float64 {
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
			}
		}
	}
	return sum
}
func TestObservedBudgetReturnsToZeroAfterRejectedAndCancelledWaiter(t *testing.T) {
	budget, err := NewObservedWorkBudget(1, 1, "other", "worker")
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"backend": "other", "stage": "worker"}
	release, err := budget.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		r, err := budget.Acquire(ctx)
		if r != nil {
			r()
		}
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for transformBudgetSample(t, "tr_tavern_transform_queue_depth", labels) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("queue was not observed")
		}
		time.Sleep(time.Millisecond)
	}
	rejectedBefore := transformBudgetSample(t, "tr_tavern_transform_queue_rejected_total", map[string]string{"backend": "other", "stage": "worker", "reason": "full"})
	if r, err := budget.Acquire(t.Context()); !errors.Is(err, ErrBusy) {
		if r != nil {
			r()
		}
		t.Fatal(err)
	}
	if transformBudgetSample(t, "tr_tavern_transform_queue_rejected_total", map[string]string{"backend": "other", "stage": "worker", "reason": "full"}) != rejectedBefore+1 {
		t.Fatal("full queue rejection missing")
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	release()
	if transformBudgetSample(t, "tr_tavern_transform_queue_depth", labels) != 0 || transformBudgetSample(t, "tr_tavern_transform_active", labels) != 0 {
		t.Fatal("cancelled waiter or output leaked metric gauge")
	}
}
