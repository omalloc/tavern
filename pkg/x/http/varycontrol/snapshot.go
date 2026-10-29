package varycontrol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"net/http"
	"slices"
	"strings"
)

const (
	SnapshotVersion  uint16 = 1
	MaxSnapshotBytes        = 16 * 1024
)

var (
	ErrSnapshotTooLarge = errors.New("Vary selector exceeds byte budget")
	ErrWildcard         = errors.New("cannot capture a wildcard Vary selector")
)

type FieldValue struct {
	Name    string   `json:"name" yaml:"name"`
	Present bool     `json:"present" yaml:"present"`
	Values  []string `json:"values" yaml:"values"`
}

type Snapshot struct {
	Version uint16       `json:"version" yaml:"version"`
	Fields  []FieldValue `json:"fields" yaml:"fields"`
}

// Capture owns its values and never modifies the request. Unknown field syntax
// is preserved byte for byte: neither commas nor value order are normalized.
func Capture(fields []string, header http.Header) (Snapshot, error) {
	names, wildcard, err := Parse(fields...)
	if err != nil {
		return Snapshot{}, err
	}
	if wildcard {
		return Snapshot{}, ErrWildcard
	}
	snapshot := Snapshot{Version: SnapshotVersion}
	size := 2 + 4 // version, field count in the fingerprint encoding
	for _, name := range names {
		values, present := fieldValues(header, name)
		size += 4 + len(name) + 1 + 4
		for _, value := range values {
			size += 4 + len(value)
			if size > MaxSnapshotBytes {
				return Snapshot{}, ErrSnapshotTooLarge
			}
		}
		if size > MaxSnapshotBytes {
			return Snapshot{}, ErrSnapshotTooLarge
		}
		snapshot.Fields = append(snapshot.Fields, FieldValue{
			Name: name, Present: present, Values: slices.Clone(values),
		})
	}
	return snapshot, nil
}

// fieldValues also handles Headers built with noncanonical map keys. Multiple
// spellings are combined in a deterministic order, preserving each line's values.
func fieldValues(header http.Header, name string) ([]string, bool) {
	keys := make([]string, 0, 1)
	for key := range header {
		if strings.EqualFold(key, name) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var values []string
	for _, key := range keys {
		values = append(values, header[key]...)
	}
	return values, len(keys) > 0
}

// Match fails closed for unknown versions and noncanonical persisted snapshots.
// Fingerprints narrow candidates; this full comparison is always required.
func Match(snapshot Snapshot, header http.Header) bool {
	if snapshot.Version != SnapshotVersion {
		return false
	}
	fields := make([]string, len(snapshot.Fields))
	for i, field := range snapshot.Fields {
		fields[i] = field.Name
	}
	actual, err := Capture(fields, header)
	if err != nil || len(actual.Fields) != len(snapshot.Fields) {
		return false
	}
	for i, expected := range snapshot.Fields {
		got := actual.Fields[i]
		if expected.Name != got.Name || expected.Present != got.Present || !slices.Equal(expected.Values, got.Values) {
			return false
		}
	}
	return true
}

// Fingerprint hashes a versioned, length-prefixed structure. Selection values
// never appear in physical keys or operational logs.
func Fingerprint(snapshot Snapshot) string {
	h := sha256.New()
	var version [2]byte
	binary.BigEndian.PutUint16(version[:], snapshot.Version)
	h.Write(version[:])
	writeCount(h, len(snapshot.Fields))
	for _, field := range snapshot.Fields {
		writeString(h, field.Name)
		if field.Present {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		writeCount(h, len(field.Values))
		for _, value := range field.Values {
			writeString(h, value)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeCount(h hash.Hash, n int) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(n))
	h.Write(buf[:])
}

func writeString(h hash.Hash, value string) {
	writeCount(h, len(value))
	h.Write([]byte(value))
}
