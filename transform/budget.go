package transform

import (
	"context"
	"errors"
	"sync"
)

var ErrBusy = errors.New("image work queue is full")

type WorkBudget struct{ active, queued chan struct{} }

func NewWorkBudget(concurrency, queue int) (*WorkBudget, error) {
	if concurrency <= 0 || concurrency > 128 || queue < 0 || queue > 4096 {
		return nil, ErrInvalidParameter
	}
	return &WorkBudget{active: make(chan struct{}, concurrency), queued: make(chan struct{}, queue)}, nil
}
func (b *WorkBudget) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case b.active <- struct{}{}:
	default:
		select {
		case b.queued <- struct{}{}:
		default:
			return nil, ErrBusy
		}
		select {
		case b.active <- struct{}{}:
			<-b.queued
		case <-ctx.Done():
			<-b.queued
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() { once.Do(func() { <-b.active }) }, nil
}
