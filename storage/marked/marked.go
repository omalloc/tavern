package marked

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/pkg/pathtrie"
)

// checker is a simple checker based on SharedKV.
// A push mark is considered present when key exists: prefix + hash.
// If AutoClear is true, the key is deleted after a hit.
type checker struct {
	KV        storagev1.SharedKV
	pathtrie  *pathtrie.PathTrie[string, int64]
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

func NewSharedKVChecker(kv storagev1.SharedKV, opts ...SharedKVOption) Checker {
	c := &checker{
		KV:        kv,
		pathtrie:  pathtrie.NewPathTrie[string, int64](),
		prefix:    "dir/",
		autoClear: true,
	}
	for _, opt := range opts {
		opt(c)
	}

	if c.KV == nil {
		panic(errors.New("SharedKVChecker requires a non-nil SharedKV"))
	}

	// 从索引里恢复前缀树数据
	if err := c.KV.IteratePrefix(context.Background(), []byte(c.prefix), func(key, val []byte) error {
		storePath := string(key[len(c.prefix):])
		if len(val) != 8 {
			log.Warnf("purge sharedKV invalid value len %d for key %s", len(val), key)
			return nil
		}
		unix := int64(binary.LittleEndian.Uint64(val))
		c.pathtrie.Insert(storePath, unix)
		log.Infof("purge reload pathtrie %s, drop-time %d", storePath, unix)
		return nil
	}); err != nil {
		log.Errorf("purge reload sharedKV failed: %v", err)
	}

	// end
	return c
}

func (c *checker) Marked(ctx context.Context, id *object.ID, md *object.Metadata) (bool, error) {
	if id == nil {
		return false, nil
	}

	// 前缀树里找有没有具体推送目录任务，以及推送时间
	unix, found1 := c.pathtrie.Search(id.Path())
	// 前缀树存在，并且 对象最后修改时间 小于等于 推送时间，说明对象在推送目录任务之前保存的，需要标记为为过期
	if found1 && md.RespUnix <= unix {
		return true, nil
	}
	return false, nil
}

func (c *checker) TrieAdd(ctx context.Context, storePath string) {
	unix := time.Now().Unix()
	// 添加到前缀树
	c.pathtrie.Insert(storePath, unix)
	// 存储到 SharedKV 里，方便重启后恢复
	if err := c.KV.Set(ctx,
		fmt.Appendf(nil, "%s%s", c.prefix, storePath),
		binary.LittleEndian.AppendUint64(nil, uint64(unix))); err != nil {
		log.Errorf("purge add sharedKV %s failed: %v", storePath, err)
		return
	}

	log.Infof("purge add pathtrie %s, drop-time %d", storePath, unix)
}
