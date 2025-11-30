package storage

import (
	"context"
	"io"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

type Selector interface {
	// Select selects the Bucket by the object ID.
	Select(ctx context.Context, id *object.ID) Bucket
	// Rebuild rebuilds the Bucket hashring.
	// do not call this method frequently.
	Rebuild(ctx context.Context, buckets []Bucket) error
}

type Operation interface {
	// Lookup retrieves the metadata for the specified object ID.
	Lookup(ctx context.Context, id *object.ID) (*object.Metadata, error)
	// Store store the metadata for the specified object ID.
	Store(ctx context.Context, meta *object.Metadata) error
	// Exist checks if the object exists.
	Exist(ctx context.Context, id []byte) bool
	// Remove soft-removes the object.
	Remove(ctx context.Context, id *object.ID) error
	// Discard hard-removes the object.
	Discard(ctx context.Context, id *object.ID) error
	// DiscardWithHash hard-removes the hash of the object.
	DiscardWithHash(ctx context.Context, hash object.IDHash) error
	// DiscardWithMessage hard-removes the object with a message.
	DiscardWithMessage(ctx context.Context, id *object.ID, msg string) error
	// DiscardWithMetadata hard-removes the object with a metadata.
	DiscardWithMetadata(ctx context.Context, meta *object.Metadata) error
	// Iterate iterates the objects.
	Iterate(ctx context.Context, fn func(*object.Metadata) error) error
	// Expired if the object is expired callback.
	Expired(ctx context.Context, id *object.ID, md *object.Metadata) bool
}

type Storage interface {
	io.Closer
	Selector

	Buckets() []Bucket

	PURGE(storeUrl string, typ PurgeControl) error
}

type Bucket interface {
	io.Closer
	Operation

	// ID returns the Bucket ID.
	ID() string
	// Weight returns the Bucket weight, range 0-1000.
	Weight() int
}

type PurgeControl struct {
	Hard        bool `json:"hard"`         // 是否硬删除, default: false
	Dir         bool `json:"dir"`          // 是否清理目录, default: false
	MarkExpired bool `json:"mark_expired"` // 是否标记为过期, default: false
}
