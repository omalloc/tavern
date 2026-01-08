package event

import (
	"context"
	"fmt"
	"sync"

	"github.com/maniartech/signals"
)

type Kind string

type TopicKey[T any] interface {
	Name() Kind
}

type topicKey[T any] struct {
	name Kind
}

func (t topicKey[T]) Name() Kind {
	return t.name
}

func NewTopicKey[T any](name Kind) TopicKey[T] {
	return topicKey[T]{name: name}
}

var (
	subscribers map[Kind]*signals.AsyncSignal[any] = make(map[Kind]*signals.AsyncSignal[any])
	lock        sync.RWMutex
)

func NewPublish[T any](topic TopicKey[T]) func(ctx context.Context, payload T) {
	lock.Lock()
	defer lock.Unlock()

	if s, ok := subscribers[topic.Name()]; ok {
		return func(ctx context.Context, payload T) {
			s.Emit(ctx, payload)
		}
	}

	sig := signals.New[any]()
	subscribers[topic.Name()] = sig

	return func(ctx context.Context, payload T) {
		sig.Emit(ctx, payload)
	}
}

func Subscribe[T any](topic TopicKey[T], handler func(ctx context.Context, payload T)) error {
	lock.Lock()
	defer lock.Unlock()

	if sig, ok := subscribers[topic.Name()]; ok {
		sig.AddListener(func(ctx context.Context, payload any) {
			handler(ctx, payload.(T))
		})
		return nil
	}

	return fmt.Errorf("topic %s not found", topic.Name())
}
