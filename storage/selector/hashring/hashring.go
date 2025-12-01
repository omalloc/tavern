package hashring

import (
	"context"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

const (
	Name            = "hashring"
	DefaultReplicas = 20
)

var _ storage.Selector = (*Balancer)(nil)

type Option func(*Balancer)

type Balancer struct {
	buckets  []storage.Bucket
	replicas int
	hashring *Consistent
}

func New(buckets []storage.Bucket, opts ...Option) (storage.Selector, error) {
	b := &Balancer{
		buckets:  buckets,
		replicas: DefaultReplicas,
	}

	for _, opt := range opts {
		opt(b)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.Rebuild(ctx, buckets)
	return b, nil
}

// Select implements storage.Selector.
func (b *Balancer) Select(ctx context.Context, id *object.ID) storage.Bucket {
	for i := 1; i <= len(b.buckets); i++ {
		groups, err := b.hashring.GetN(string(id.Bytes()), i)
		if err != nil {
			return nil
		}

		bucket := groups[i-1].(storage.Bucket)
		// use percent below HighPercent.
		if bucket.UseAllow() {
			if bucket.HasBad() {
				continue
			}
			return bucket
		}
	}
	return nil
}

// Rebuild implements storage.Selector.
func (b *Balancer) Rebuild(ctx context.Context, buckets []storage.Bucket) error {
	newBuckets := make([]Node, 0, len(buckets))
	for _, z := range buckets {
		newBuckets = append(newBuckets, z)
	}

	b.buckets = buckets
	b.hashring = NewConsistent(newBuckets, b.replicas)
	return nil
}

// WithReplicas ...
func WithReplicas(replicas int) Option {
	return func(b *Balancer) {
		b.replicas = replicas
	}
}
