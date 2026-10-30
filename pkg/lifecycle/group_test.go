package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainCancelsButWaitsForExplicitCompletion(t *testing.T) {
	var group Group
	parent := context.WithValue(t.Context(), struct{}{}, "value")
	ctx, finish, err := group.Begin(parent, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithCancel(t.Context())
	cancel()
	if err = group.Drain(short); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation counted as completed work", err)
	}
	if ctx.Err() == nil || ctx.Value(struct{}{}) != "value" {
		t.Fatal("lost cancellation or request values")
	}
	if _, _, err = group.Begin(t.Context(), time.Second); !errors.Is(err, ErrDraining) {
		t.Fatal("accepted work after drain", err)
	}
	finish()
	finish()
	if err = group.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
}
