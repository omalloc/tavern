package selector

import (
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/storage/selector/hashring"
)

var registrySelector = map[string]storage.Selector{
	"hashring": nil,
}

func New(buckets []storage.Bucket, typ string) storage.Selector {
	curr, err := hashring.New(buckets, hashring.WithReplicas(20))
	if err != nil {
		panic(err)
	}
	return curr
}
