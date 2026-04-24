//go:build !linux
// +build !linux

package rawdisk

import (
	"errors"
	"github.com/omalloc/tavern/api/defined/v1/storage"
)

func New(opt *storage.BucketConfig, sharedkv storage.SharedKV) (storage.Bucket, error) {
	return nil, errors.New("rawdisk bucket is only supported on Linux")
}
