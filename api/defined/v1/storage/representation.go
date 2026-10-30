package storage

import (
	"context"
	"errors"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var (
	ErrControlUnsupported    = errors.New("representation control records are unsupported")
	ErrProtectedControl      = errors.New("resource controls require the representation coordinator")
	ErrDurabilityUnsupported = errors.New("durable representation storage is unsupported")
)

// ControlStore is an optional bucket capability. Control writes replace a whole
// record without merging chunks, touching LRU, changing domain counters, or
// registering the root in the ordinary object directory. A single coordinator
// owns updates. This base capability alone does not promise crash durability;
// use DurableControlStore for synchronous invalidation acknowledgements.
type ControlStore interface {
	LoadControl(context.Context, *object.ID) (*object.Metadata, error)
	StoreControl(context.Context, *object.Metadata) error
}

// SyncIndexDB is optional. SetSync must sync the encoded record and its WAL
// before success, independent of ordinary Set configuration. Persistent is false
// for memory filesystems and backends without a verified synchronous path.
type SyncIndexDB interface {
	SetSync(context.Context, []byte, *object.Metadata) error
	Persistent() bool
}

type DurableControlStore interface {
	ControlStore
	CheckDurability() error
	StoreControlSync(context.Context, *object.Metadata) error
}

// DurableRepresentationBucket syncs immutable files outside resource locks,
// then final child metadata under the commit lock before publishing the root.
type DurableRepresentationBucket interface {
	Bucket
	CheckDurability() error
	SyncRepresentationData(context.Context, *object.Metadata) error
	StoreRepresentationSync(context.Context, *object.Metadata) error
}
