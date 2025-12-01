package indexdb

import (
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/pkg/mapstruct"
)

var defaultRegistry = NewRegistry()

type option struct {
	codec  encoding.Codec
	dbType string
	dbPath string
	dbName string
}

type Option func(*option)

func WithCodec(codec encoding.Codec) Option {
	return func(o *option) {
		o.codec = codec
	}
}

func WithType(dbType string) Option {
	return func(o *option) {
		o.dbType = dbType
	}
}

func NewOption(path string, opts ...Option) storage.Option {
	r := &option{
		codec:  encoding.GetDefaultCodec(),
		dbPath: path,
	}
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// DBType implements Option.
func (o *option) DBType() string {
	return o.dbType
}

// DBPath implements Option.
func (o *option) DBPath() string {
	return o.dbPath
}

func (o *option) DBName() string {
	return o.dbName
}

// Codec implements Option.
func (o *option) Codec() encoding.Codec {
	return o.codec
}

// Unmarshal implements Option.
func (o *option) Unmarshal(v interface{}) error {
	return mapstruct.Decode(o, v)
}
