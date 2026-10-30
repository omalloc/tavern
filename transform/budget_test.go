package transform

import (
	"context"
	"errors"
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
