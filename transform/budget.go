package transform

import (
	"context"
	"errors"
	"sync"

	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
)

var ErrBusy = errors.New("image work queue is full")

type WorkBudget struct {
	active, queued chan struct{}
	backend, stage string
}

func NewWorkBudget(concurrency, queue int) (*WorkBudget, error) {
	if concurrency <= 0 || concurrency > 128 || queue < 0 || queue > 4096 {
		return nil, ErrInvalidParameter
	}
	return &WorkBudget{active: make(chan struct{}, concurrency), queued: make(chan struct{}, queue)}, nil
}
func NewObservedWorkBudget(concurrency, queue int, backend, stage string) (*WorkBudget, error) {
	if stage != "task" && stage != "worker" {
		return nil, ErrInvalidParameter
	}
	budget, err := NewWorkBudget(concurrency, queue)
	if err != nil {
		return nil, err
	}
	if backend != "http" {
		backend = "other"
	}
	budget.backend, budget.stage = backend, stage
	budget.observe(0, 0)
	return budget, nil
}
func (b *WorkBudget) observe(queued, active float64) {
	if b.stage != "" {
		pkgmetrics.TransformBudget(b.backend, b.stage, queued, active)
	}
}
func (b *WorkBudget) reject(err error) {
	if b.stage == "" {
		return
	}
	reason := "full"
	if errors.Is(err, context.Canceled) {
		reason = "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	}
	pkgmetrics.RecordTransformRejected(b.backend, b.stage, reason)
}
func (b *WorkBudget) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		b.reject(err)
		return nil, err
	}
	select {
	case b.active <- struct{}{}:
	default:
		select {
		case b.queued <- struct{}{}:
		default:
			b.reject(ErrBusy)
			return nil, ErrBusy
		}
		b.observe(1, 0)
		defer b.observe(-1, 0)
		select {
		case b.active <- struct{}{}:
			<-b.queued
		case <-ctx.Done():
			<-b.queued
			b.reject(ctx.Err())
			return nil, ctx.Err()
		}
	}
	b.observe(0, 1)
	var once sync.Once
	return func() { once.Do(func() { <-b.active; b.observe(0, -1) }) }, nil
}
