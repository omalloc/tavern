package storage

import (
	"context"
	"errors"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var (
	ErrControlUnsupported = errors.New("representation control records are unsupported")
	ErrProtectedControl   = errors.New("resource controls require the representation coordinator")
)

// ControlStore is an optional bucket capability. Control writes replace a whole
// record without merging chunks, touching LRU, changing domain counters, or
// registering the root in the ordinary object directory. A single coordinator
// owns updates. This base capability alone does not promise crash durability;
// durable invalidation barriers and synchronous writes are added in P2.
type ControlStore interface {
	LoadControl(context.Context, *object.ID) (*object.Metadata, error)
	StoreControl(context.Context, *object.Metadata) error
}
