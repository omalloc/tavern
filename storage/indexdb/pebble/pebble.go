package pebble

import (
	"github.com/cockroachdb/pebble/v2"

	"github.com/omalloc/tavern/pkg/encoding"
)

type PebbleDB struct {
	codec         encoding.Codec
	db            *pebble.DB
	skipErrRecord bool
}
