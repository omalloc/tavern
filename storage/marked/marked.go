package marked

import (
	"context"
	"errors"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/pathtire"
)

// checker is a simple checker based on SharedKV.
// A push mark is considered present when key exists: prefix + hash.
// If AutoClear is true, the key is deleted after a hit.
type checker struct {
	KV        storagev1.SharedKV
	pathtire  *pathtire.PathTrie[string]
	prefix    string
	autoClear bool
}

type SharedKVOption func(*checker)

func WithPrefix(prefix string) SharedKVOption {
	return func(c *checker) {
		c.prefix = prefix
	}
}

func WithAutoClear(clear bool) SharedKVOption {
	return func(c *checker) {
		c.autoClear = clear
	}
}

func NewSharedKVChecker(kv storagev1.SharedKV, opts ...SharedKVOption) *checker {
	c := &checker{
		KV:        kv,
		pathtire:  pathtire.NewPathTrie[string](),
		prefix:    "pdir/",
		autoClear: true,
	}
	for _, opt := range opts {
		opt(c)
	}

	if c.KV == nil {
		panic(errors.New("SharedKVChecker requires a non-nil SharedKV"))
	}
	return c
}

func (c *checker) Marked(ctx context.Context, id *object.ID, _ *object.Metadata) (bool, error) {
	if id == nil {
		return false, nil
	}

	// 前缀树里找有没有具体推送目录任务
	_, found1 := c.pathtire.Search(id.Path())
	if found1 {
		return true, nil
	}
	return false, nil
}

func (c *checker) TireAdd(ctx context.Context, storePath string) {
	c.pathtire.Insert(storePath, "")
	log.Infof("purge add pathtire %s", storePath)
}
