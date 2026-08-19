package varycontrol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"slices"
	"sort"
	"strings"
)

const NormalizationVersion uint16 = 1

type Key []string

func (k Key) String() string         { return strings.Join(k, ",") }
func (k *Key) Append(value string)   { *k = Clean(append([]string(*k), value)...) }
func (k Key) Compare(other Key) bool { return slices.Equal(k, other) }
func (k Key) Wildcard() bool         { return slices.Contains(k, "*") }

func Clean(values ...string) Key { return CleanWithIgnore(nil, values...) }

func CleanWithIgnore(ignore map[string]struct{}, values ...string) Key {
	var keys []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			if name != "*" {
				if _, ok := ignore[name]; ok {
					continue
				}
			}
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// SelectorDigest keeps field-line and comma-member order and distinguishes a
// missing field from an explicitly present empty field.
func (k Key) SelectorDigest(h http.Header) string {
	hash := sha256.New()
	writeUint16(hash.Write, NormalizationVersion)
	writeUint32(hash.Write, uint32(len(k)))
	for _, field := range k {
		writeBytes(hash.Write, []byte(field))
		values, present := headerValues(h, field)
		if present {
			_, _ = hash.Write([]byte{1})
		} else {
			_, _ = hash.Write([]byte{0})
		}
		writeUint32(hash.Write, uint32(len(values)))
		for _, value := range values {
			writeBytes(hash.Write, normalize(field, value))
		}
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func normalize(field, value string) []byte {
	value = strings.Trim(value, " \t")
	if field != "accept-encoding" {
		return []byte(value)
	}
	// Content-coding names and parameter names are case-insensitive. Keep the
	// member order and qvalue intact while removing only insignificant OWS.
	members := strings.Split(value, ",")
	for i, member := range members {
		params := strings.Split(member, ";")
		params[0] = strings.ToLower(strings.TrimSpace(params[0]))
		for j := 1; j < len(params); j++ {
			param := strings.TrimSpace(params[j])
			if name, val, ok := strings.Cut(param, "="); ok {
				params[j] = strings.ToLower(strings.TrimSpace(name)) + "=" + strings.TrimSpace(val)
			} else {
				params[j] = strings.ToLower(param)
			}
		}
		members[i] = strings.Join(params, ";")
	}
	return []byte(strings.Join(members, ","))
}

func (k Key) ID() string {
	hash := sha256.New()
	writeUint16(hash.Write, NormalizationVersion)
	for _, field := range k {
		writeBytes(hash.Write, []byte(field))
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil)[:12])
}

// VaryData remains for compatibility with v1 callers.
func (k Key) VaryData(h http.Header) string {
	if len(k) == 0 {
		return ""
	}
	return k.SelectorDigest(h)
}

func headerValues(h http.Header, field string) ([]string, bool) {
	for name, values := range h {
		if strings.EqualFold(name, field) {
			return values, true
		}
	}
	return nil, false
}

type writer func([]byte) (int, error)

func writeUint16(w writer, value uint16) {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], value)
	_, _ = w(buf[:])
}
func writeUint32(w writer, value uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], value)
	_, _ = w(buf[:])
}
func writeBytes(w writer, value []byte) {
	writeUint32(w, uint32(len(value)))
	_, _ = w(value)
}
