package object

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

// IdHashSize is the size of the byte array that contains the object hash.
const IdHashSize = sha1.Size

// IDHash is the fixed-width byte array that represents an ObjectID hash.
type IDHash [IdHashSize]byte

type ID struct {
	path string
	ext  string
	hash IDHash

	cacheID string `json:"-" yaml:"-"`
}

func (id *ID) String() string {
	return id.cacheID
}

// Key returns the concatenation of the path and ext of the ID.
func (id *ID) Key() string {
	return id.path + id.ext
}

// Path returns the path of the ID.
func (id *ID) Path() string {
	return id.path
}

// Ext returns the ext of the ID.
func (id *ID) Ext() string {
	return id.ext
}

func (id *ID) Hash() IDHash {
	return id.hash
}

func (id *ID) HashStr() string {
	return hex.EncodeToString(id.hash[:])
}

func (id *ID) Bytes() []byte {
	return id.hash[:]
}

// WPath returns the read/write path of the object ID.
// dir F/FF/hash with path.
func (id *ID) WPath(pwd string) string {
	hash := hex.EncodeToString(id.hash[:])
	return filepath.Join(pwd, hash[0:1], hash[2:4], hash)
}

func (idx IDHash) WPath(pwd string) string {
	h := hex.EncodeToString(idx[:])
	return filepath.Join(pwd, h[0:1], h[2:4], h)
}

func NewID(path string) *ID {
	hash := sha1.Sum([]byte(path))
	return &ID{
		path:    path,
		ext:     "",
		hash:    hash,
		cacheID: fmt.Sprintf("{%x:%s%s}", hash, path, ""),
	}
}

func NewVirtualID(path string, virtualKey string) *ID {
	hash := sha1.Sum([]byte(path + virtualKey))
	return &ID{
		path:    path,
		ext:     virtualKey,
		hash:    hash,
		cacheID: fmt.Sprintf("{%x:%s%s}", hash, path, virtualKey),
	}
}
