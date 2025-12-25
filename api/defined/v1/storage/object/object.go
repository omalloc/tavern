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

	ID          *ID           `json:"id"`             // object ID
	BlockSize   uint64        `json:"bsize"`          // block size
	Chunks      bitmap.Bitmap `json:"chunks"`         // file chunk
	Parts       bitmap.Bitmap `json:"parts"`          // file chunk parts
	Code        int           `json:"code"`           // http response code
	Size        uint64        `json:"size"`           // object size
	RespUnix    int64         `json:"resp_unix"`      // response time
	LastRefUnix int64         `json:"last_ref_unix"`  // last reference time
	Refs        int64         `json:"refs"`           // reference count
	ExpiresAt   int64         `json:"expires_at"`     // expiration time
	Headers     http.Header   `json:"headers"`        // http headers
	VirtualKey  []string      `json:"vkey,omitempty"` // vary keys
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

func (m *Metadata) HasComplete() bool {
	if m.IsVary() {
		return false
	}
	if m.Size <= 0 {
		return false
	}

	n := m.Size / m.BlockSize
	if m.Size%m.BlockSize != 0 {
		n++
	}
	return n == uint64(m.Parts.Count())
}

// Clone clones the metadata.
func (m *Metadata) Clone() *Metadata {
	return &Metadata{
		ID:          m.ID,
		BlockSize:   m.BlockSize,
		Chunks:      m.Chunks.Clone(nil),
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
