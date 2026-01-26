package storage

import (
	"context"
	"errors"
	"io"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/encoding"
)

var ErrKeyNotFound = errors.New("key not found")

const (
	TypeInMemory = "inmemory"
	TypeNormal   = "normal" // normal, warm 同一个
	TypeWarm     = "warm"   // normal, warm 同一个
	TypeCold     = "cold"
	TypeHot      = "hot"
)

type IterateFunc func(key []byte, val *object.Metadata) bool

// IndexDB represents the interface for metadata storage operations
type IndexDB interface {
	io.Closer

	// Get retrieves metadata for the given key from the IndexDB
	// Returns the metadata object if found, or error if not found or on failure
	Get(ctx context.Context, key []byte) (*object.Metadata, error)

	// Set stores or updates metadata for the given key in the IndexDB
	// Returns error if the operation fails
	Set(ctx context.Context, key []byte, val *object.Metadata) error

	// Exist checks if metadata exists for the given key
	// Returns true if the key exists, false otherwise
	Exist(ctx context.Context, key []byte) bool

	// Delete removes metadata for the given key from the IndexDB
	// Returns error if the operation fails
	Delete(ctx context.Context, key []byte) error

	// Iterate walks through all metadata entries with the given prefix
	// Calls the provided function f for each entry found
	// Returns error if the iteration fails
	Iterate(ctx context.Context, prefix []byte, f IterateFunc) error

	// Expired iterates through expired metadata entries
	// Calls the provided function f for each expired entry
	// Returns error if the iteration fails
	Expired(ctx context.Context, f IterateFunc) error

	// GC performs garbage collection on the IndexDB
	// Returns error if the operation fails
	GC(ctx context.Context) error
}

// IndexDBFactory is a function that creates a new IndexDB instance
// It takes a path and an Option as arguments
// Returns the created IndexDB instance and an error if the operation fails
type IndexDBFactory func(path string, option Option) (IndexDB, error)

// Option is an interface for configuring the IndexDB options
type Option interface {
	// DBType returns the type of the IndexDB
	DBType() string
	// DBPath returns the path to the IndexDB file
	DBPath() string
	// Codec returns the codec for encoding and decoding metadata
	Codec() encoding.Codec
	// Unmarshal decodes the given data into the provided interface
	Unmarshal(v interface{}) error
}
