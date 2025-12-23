package varycontrol

import (
	"net/http"
	"slices"
	"sort"
	"strings"
)

const (
	VaryEmptyIdentity = "tr_identity"
)

type Key []string

func (k *Key) String() string {
	return strings.Join(*k, ",")
}

func (k *Key) Append(val string) {
	keys := canonical(val)
	if len(keys) > 0 {
		if slices.Contains(*k, val) {
			return
		}
		*k = append(*k, keys...)
	}
}

func (k *Key) VaryData(h http.Header) string {
	l := len(*k)
	if l <= 0 {
		return ""
	}

	kv := make(map[string]string, l)
	for _, key := range *k {
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
		v := kv[key]
		if buf.Len() > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(v)
	}
	return buf.String()
}

func Clean(values ...string) Key {
	keys := make([]string, 0)

	for _, val := range values {
		key := canonical(val)
		if len(key) > 0 {
			keys = append(keys, key...)
		}
	}

	sort.Strings(keys)

	return slices.Compact(keys)
}

func canonical(val string) []string {
	s := strings.TrimSpace(val)
	if s == "" || s == "," {
		return nil
	}

	keys := make([]string, 0)

	vk := strings.Split(s, ",")
	for _, k := range vk {
		key := strings.TrimSpace(k)
		if key != "" {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return nil
	}

	return keys
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

	v := splitTrimSpace(val)

	sort.Strings(v)

	return strings.Join(v, ",")
}

func splitTrimSpace(s string) []string {
	if s == "" {
		return nil
	}

	v := strings.Split(s, ",")
	for i := range v {
		v[i] = strings.TrimSpace(v[i])
	}

	return v
}
