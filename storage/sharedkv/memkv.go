package sharedkv

import (
	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/contrib/log"
)

// NewMemSharedKV create a new memory kv store
func NewMemSharedKV() storage.SharedKV {
	opts := &pebble.Options{
		FS:         vfs.NewMem(),
		DisableWAL: true,
		Logger:     log.NewHelper(log.NewFilter(log.GetLogger(), log.FilterLevel(log.LevelWarn))),
	}

	db, err := newNoneKV("", opts)
	if err != nil {
		panic(err)
	}

	return db
}
