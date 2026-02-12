package storage

import (
	"errors"
	"path"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/conf"
	"github.com/omalloc/tavern/storage/bucket/disk"
	"github.com/omalloc/tavern/storage/bucket/empty"
	"github.com/omalloc/tavern/storage/bucket/memory"
	_ "github.com/omalloc/tavern/storage/indexdb/nutsdb"
	_ "github.com/omalloc/tavern/storage/indexdb/pebble"
)

type globalBucketOption struct {
	AsyncLoad       bool
	EvictionPolicy  string
	SelectionPolicy string
	Driver          string
	DBType          string
	DBPath          string
	Migration       *storage.MigrationConfig
}

// implements storage.Bucket map.
var bucketMap = map[string]func(opt *storage.BucketConfig, sharedkv storage.SharedKV) (storage.Bucket, error){
	"empty":  empty.New,
	"native": disk.New,   // disk is an alias of native
	"memory": memory.New, // in-memory disk. restart as lost. @ storage.TypeInMemory
}

func NewBucket(opt *storage.BucketConfig, sharedkv storage.SharedKV) (storage.Bucket, error) {
	factory, exist := bucketMap[opt.Driver]
	if !exist {
		return nil, errors.New("bucket factory not found")
	}
	return factory(opt, sharedkv)
}

func mergeConfig(global *globalBucketOption, bucket *conf.Bucket) *storage.BucketConfig {
	// copied from conf bucket.
	copied := &storage.BucketConfig{
		Path:           bucket.Path,
		Driver:         bucket.Driver,
		Type:           bucket.Type,
		DBType:         bucket.DBType,
		DBPath:         bucket.DBPath,
		MaxObjectLimit: bucket.MaxObjectLimit,
		Migration:      global.Migration, // migration config
		DBConfig:       bucket.DBConfig,  // custom db config
	}

	if copied.Driver == "" {
		copied.Driver = global.Driver
	}
	if copied.Type == "" {
		copied.Type = storage.TypeWarm
	}
	// replace normal to warm
	if copied.Type == storage.TypeNormal {
		copied.Type = storage.TypeWarm
	}
	if copied.DBType == "" {
		copied.DBType = global.DBType
	}
	if copied.MaxObjectLimit <= 0 {
		copied.MaxObjectLimit = 10_000_000 // default 10 million objects
	}

	// set default db_path
	if copied.DBPath == "" {
		copied.DBPath = global.DBPath
	}
	if !path.IsAbs(copied.DBPath) {
		copied.DBPath = path.Join(bucket.Path, copied.DBPath)
	}

	return copied
}
