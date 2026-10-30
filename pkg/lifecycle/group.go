// Package lifecycle tracks accepted work until its owner has actually finished.
package lifecycle

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrDraining = errors.New("storage is draining")

// Group is ready to use at its zero value. Drain permanently closes admission,
// cancels accepted contexts and waits for their explicit finish callbacks.
// Cancellation alone cannot prove local writers or file handles have stopped.
type Group struct {
	mu      sync.Mutex
	stopped bool
	active  map[*task]struct{}
	drained chan struct{}
}
type task struct {
	cancel context.CancelFunc
	once   sync.Once
}

func (g *Group) Begin(parent context.Context, timeout time.Duration) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		cancel()
		return nil, nil, ErrDraining
	}
	if g.active == nil {
		g.active = make(map[*task]struct{})
		g.drained = make(chan struct{})
	}
	t := &task{cancel: cancel}
	g.active[t] = struct{}{}
	g.mu.Unlock()
	finish := func() {
		t.once.Do(func() {
			cancel()
			g.mu.Lock()
			delete(g.active, t)
			if g.stopped && len(g.active) == 0 {
				close(g.drained)
			}
			g.mu.Unlock()
		})
	}
	return ctx, finish, nil
}

func (g *Group) Drain(ctx context.Context) error {
	g.mu.Lock()
	if g.drained == nil {
		g.drained = make(chan struct{})
	}
	if !g.stopped && len(g.active) == 0 {
		close(g.drained)
	}
	g.stopped = true
	cancels := make([]context.CancelFunc, 0, len(g.active))
	for t := range g.active {
		cancels = append(cancels, t.cancel)
	}
	done := g.drained
	g.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
