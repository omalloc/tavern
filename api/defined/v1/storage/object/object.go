package object

import (
	"net/http"

	"github.com/kelindar/bitmap"
)

type CacheFlag int8

const (
	FlagCache        CacheFlag = 0        // normal cache index
	FlagVaryIndex    CacheFlag = 1 << 0   // vary index
	FlagVaryCache    CacheFlag = 0x1 << 1 // vary cache
	FlagChunkedCache CacheFlag = 0x1 << 2 // chunked index
)

type Metadata struct {
	Flags CacheFlag `json:"flags"`

	ID          *ID           `json:"id" yaml:"id"`
	Parts       bitmap.Bitmap `json:"parts"`
	Code        int           `json:"code"`
	Size        uint64        `json:"size"`
	RespUnix    int64         `json:"resp_unix"`
	LastRefUnix int64         `json:"last_ref_unix"`
	Refs        int64         `json:"refs"`
	ExpiresAt   int64         `json:"expires_at"`
	Headers     http.Header   `json:"headers"`
	VirtualKey  []string      `json:"vkey,omitempty"`
}

// IsVary returns true if the metadata is a vary metadata.
func (m *Metadata) IsVary() bool {
	return m.Flags == FlagVaryIndex
}

// IsVaryCache returns true if the metadata is a vary-cache metadata.
func (m *Metadata) IsVaryCache() bool {
	return m.Flags&FlagVaryCache > 0
}

// IsChunked returns true if the metadata is a chunked metadata.
func (m *Metadata) IsChunked() bool {
	return m.Flags&FlagChunkedCache > 0
}

// IsVaryChunked returns true if the metadata is a vary-cache-chunked metadata.
func (m *Metadata) IsVaryChunked() bool {
	return m.IsVaryCache() && m.IsChunked()
}

// HasVary returns true if the metadata has vary keys.
func (m *Metadata) HasVary() bool {
	return len(m.VirtualKey) > 0
}

// Clone clones the metadata.
func (m *Metadata) Clone() *Metadata {
	return &Metadata{
		ID:          m.ID,
		Parts:       m.Parts.Clone(nil),
		Code:        m.Code,
		Size:        m.Size,
		RespUnix:    m.RespUnix,
		LastRefUnix: m.LastRefUnix,
		Refs:        m.Refs,
		ExpiresAt:   m.ExpiresAt,
		Headers:     m.Headers.Clone(),
		Flags:       m.Flags,
		VirtualKey:  append([]string{}, m.VirtualKey...),
	}
}
