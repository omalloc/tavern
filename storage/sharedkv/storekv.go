package sharedkv

import (
	"github.com/cockroachdb/pebble/v2"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/contrib/log"
)

// NewStoreSharedKV create a new store kv store
func NewStoreSharedKV(storePath string) storage.SharedKV {
	db, err := newNoneKV(storePath, &pebble.Options{
		DisableWAL: true,
		Logger:     log.NewHelper(log.NewFilter(log.GetLogger(), log.FilterLevel(log.LevelWarn))),
	})
	if err != nil {
		panic(err)
	}

	return db
}
