package storage

import (
	"context"
	"errors"
	"time"

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
	// Persist cleanup ownership before creating chunk bytes, including Parts
	// and unknown-length filling bodies that are not readable descriptors yet.
	StoreRepresentationJournalSync(context.Context, *object.Metadata) error
}

var (
	ErrInvalidPurge       = errors.New("invalid purge target or control")
	ErrFillSuperseded     = errors.New("legacy fill superseded by invalidation")
	ErrInvalidationBudget = errors.New("representation invalidation budget exhausted")
)

// RepresentationPurger is registered once for each control owner, covering all
// namespaces it manages. Success acknowledges the logical invalidation barrier.
type RepresentationPurger interface {
	Purge(context.Context, string, PurgeControl) error
}

type RepresentationRegistry interface {
	RegisterRepresentationPurger(string, RepresentationPurger) error
}

// LegacyFillLease fences older format writers against PURGE in the same process.
// Guard covers only local storage I/O, never an origin request or body Read.
type LegacyFillLease interface {
	Guard(context.Context, func() error) error
	Renew(context.Context) (LegacyFillLease, bool, error)
	Close() error
}

type LegacyFillCoordinator interface {
	BeginLegacyFill(context.Context, string) (LegacyFillLease, error)
}

// MetadataScanner is a bounded raw scan for recovery and collection. It has no
// LRU/Touch side effects and includes protected controls. Cursor is exclusive;
// a nil next cursor means this pass reached the end. Unreadable encodings are errors.
type MetadataScanner interface {
	ScanMetadata(context.Context, []byte, int) ([]*object.Metadata, []byte, error)
}

type IndexScanner interface {
	Scan(context.Context, []byte, int) ([]*object.Metadata, []byte, error)
}

// RepresentationEviction routes ordinary bucket pressure through the owning
// coordinator. Missing handlers must never directly discard v2 metadata/bytes.
type RepresentationEviction interface {
	SetRepresentationEvictor(func(context.Context, *object.Metadata) error)
}

// RepresentationBucketProvider exposes fixed raw buckets through compatibility
// decorators. Dynamic health selectors cannot choose control ownership.
type RepresentationBucketProvider interface {
	RepresentationBuckets() []Bucket
}

// TaskCoordinator fences request/background work before closing storage.
// The returned finish must run after response readers and async writers close.
type TaskCoordinator interface {
	BeginCacheTask(context.Context, time.Duration) (context.Context, func(), error)
	Drain(context.Context) error
}

type Quiescer interface{ Quiesce(context.Context) error }
