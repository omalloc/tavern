package varycontrol

import (
	"encoding/json"
	"net/http"
	"net/textproto"
	"slices"
	"sort"
	"strings"
)

const (
	VaryEmptyIdentity = "tr_identity"
	dataVersion       = "v2:"
)

type Key []string

type selectedHeader struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Value   string `json:"value,omitempty"`
}

func (k Key) String() string {
	return strings.Join(k, ",")
}

// Append adds canonical Vary field names while preserving API compatibility
// with the original key helper.
func (k *Key) Append(value string) {
	for _, key := range Clean(value) {
		if !slices.Contains(*k, key) {
			*k = append(*k, key)
		}
	}
}

// HasWildcard reports whether the Vary field contains "*". Such responses
// must not be reused from a cache for a later request.
func (k Key) HasWildcard() bool {
	return slices.Contains(k, "*")
}

// VaryData serializes the selected request fields without delimiter
// ambiguity. Header names are case-insensitive, field order is stable, and an
// absent field remains distinct from a present field with an empty value.
func (k Key) VaryData(h http.Header) string {
	if len(k) == 0 {
		return ""
	}

	fields := make([]selectedHeader, 0, len(k))
	for _, key := range k {
		values, present := headerValues(h, key)
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
		}
		fields = append(fields, selectedHeader{
			Name:    strings.ToLower(key),
			Present: present,
			Value:   strings.Join(values, ","),
		})
	}

	data, _ := json.Marshal(fields)
	return dataVersion + string(data)
}

// LegacyVaryData reproduces the pre-v2 key format. It is retained only so
// caches written by older Tavern versions remain readable during migration.
func (k Key) LegacyVaryData(h http.Header) string {
	l := len(k)
	if l == 0 {
		return ""
	}

	kv := make(map[string]string, l)
	for _, key := range k {
		v := sortValues(h.Values(key))
		kv[key] = v
	}

	keys := make([]string, 0, len(kv))
	for key := range kv {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf strings.Builder
	for _, key := range keys {
		if buf.Len() > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(kv[key])
	}
	return buf.String()
}

func (k Key) Compare(other Key) bool {
	return slices.Equal(k, other)
}

// Clean parses, canonicalizes, sorts, and de-duplicates Vary field names.
// Field names are case-insensitive per HTTP semantics.
func Clean(values ...string) Key {
	keys := make([]string, 0)
	for _, value := range values {
		for _, key := range split(value) {
			if key == "*" {
				keys = append(keys, key)
				continue
			}
			keys = append(keys, textproto.CanonicalMIMEHeaderKey(key))
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})
	return slices.CompactFunc(keys, strings.EqualFold)
}

// LegacyClean preserves the original field-name casing used in legacy cache
// keys while retaining the old sort and de-duplication behavior.
func LegacyClean(values ...string) Key {
	keys := make([]string, 0)
	for _, value := range values {
		keys = append(keys, split(value)...)
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

func split(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "," {
		return nil
	}

	parts := strings.Split(value, ",")
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		if key := strings.TrimSpace(part); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func headerValues(h http.Header, name string) ([]string, bool) {
	matchingNames := make([]string, 0, 1)
	for key := range h {
		if strings.EqualFold(key, name) {
			matchingNames = append(matchingNames, key)
		}
	}
	if len(matchingNames) == 0 {
		return nil, false
	}

	sort.Strings(matchingNames)
	values := make([]string, 0, len(matchingNames))
	for _, key := range matchingNames {
		values = append(values, h[key]...)
	}
	return values, true
}

func sortValues(vals []string) string {
	if len(vals) == 1 {
		return sortValue(vals[0])
	}

	v := make([]string, 0, len(vals))
	for _, val := range vals {
		v = append(v, sortValue(val))
	}
	sort.Strings(v)
	return strings.Join(v, ",")
}

func sortValue(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}

	v := strings.Split(val, ",")
	for i := range v {
		v[i] = strings.TrimSpace(v[i])
	}
	sort.Strings(v)
	return strings.Join(v, ",")
}
