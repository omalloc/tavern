package eviction

import (
	"fmt"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/algorithm/gdsf"
	"github.com/omalloc/tavern/pkg/algorithm/lru"
)

type EvictionPolicy func(cap int) storagev1.CacheReplacementPolicy[object.IDHash, storagev1.Mark]

var registry = map[string]EvictionPolicy{
	"lru": func(cap int) storagev1.CacheReplacementPolicy[object.IDHash, storagev1.Mark] {
		return lru.New[object.IDHash, storagev1.Mark](cap)
	},
	"gdsf": func(cap int) storagev1.CacheReplacementPolicy[object.IDHash, storagev1.Mark] {
		return gdsf.New[object.IDHash, storagev1.Mark](cap)
	},
}

func NewEvictionPolicy(typ string, cap int) storagev1.CacheReplacementPolicy[object.IDHash, storagev1.Mark] {
	factory, exist := registry[typ]
	if !exist {
		panic(fmt.Errorf("eviction policy %s unimplemented", typ))
	}

	return factory(cap)
}