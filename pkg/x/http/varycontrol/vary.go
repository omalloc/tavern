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

// Key is a sorted, deduplicated list of Vary header names.
// Callers should always produce keys via Clean or Append; both maintain sort
// order so that hot-path consumers (VaryData, Compare) can avoid a sort pass.
type Key []string

func (k *Key) String() string {
	return strings.Join(*k, ",")
}

// Append adds one or more header names (comma-separated) to the key set,
// skipping duplicates. It re-sorts after insert; Append is a write-path
// operation so this sort cost is acceptable.
func (k *Key) Append(val string) {
	keys := canonical(val)
	if len(keys) == 0 {
		return
	}
	for _, key := range keys {
		if slices.Contains(*k, key) {
			continue
		}
		*k = append(*k, key)
	}
	sort.Strings(*k)
}

// VaryData builds a deterministic cache-key suffix from the request headers
// specified by this Key.  The result is guaranteed to be the same for any two
// requests whose Vary-relevant header values are semantically equivalent.
//
//   - Missing headers get the sentinel VaryEmptyIdentity so that optional
//     headers do not cause variant explosion.
//   - Accept-Encoding is further normalized (quality-factor stripping, token
//     sorting, identity removal) to match ATS/Squid behaviour.
//
// VaryData assumes the Key receiver is sorted; Clean and Append both maintain
// that invariant.
func (k *Key) VaryData(h http.Header) string {
	l := len(*k)
	if l <= 0 {
		return ""
	}

	var buf strings.Builder
	for _, key := range *k {
		v := sortValues(h.Values(key))
		if v == "" {
			v = VaryEmptyIdentity
		}
		if strings.EqualFold(key, "Accept-Encoding") {
			v = normalizeAcceptEncoding(v)
			if v == "" {
				v = VaryEmptyIdentity
			}
		}
		if buf.Len() > 0 {
			buf.WriteByte('&')
		}
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(v)
	}
	return buf.String()
}

// FilterIgnore returns a sub-slice of k with every header name listed in
// ignore removed. The backing array is shared with k; the caller must not
// retain the original slice if it intends to mutate.
func (k Key) FilterIgnore(ignore map[string]struct{}) Key {
	if len(ignore) == 0 || len(k) == 0 {
		return k
	}
	n := 0
	for _, key := range k {
		if _, ok := ignore[key]; !ok {
			k[n] = key
			n++
		}
	}
	return k[:n]
}

func (k Key) Compare(k2 Key) bool {
	if len(k) != len(k2) {
		return false
	}

	for i, vk1 := range k {
		if vk1 != k2[i] {
			return false
		}
	}
	return true
}

// Clean returns a sorted, deduplicated Key from a set of raw header values
// (each may be a comma-separated list).
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

	// Fast path: a single token with no commas — skip split/sort/join.
	if !strings.Contains(val, ",") {
		return val
	}

	v := splitTrimSpace(val)

	// Filter out empty tokens that can arise from malformed values like ","
	// or leading/trailing commas (",gzip,br" → "" "gzip" "br").
	v = slices.DeleteFunc(v, func(s string) bool { return s == "" })
	if len(v) == 0 {
		return ""
	}

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

// normalizeAcceptEncoding strips quality factors (;q=...) from Accept-Encoding
// tokens and returns a sorted, canonical encoding list. This ensures that
// semantically equivalent Accept-Encoding values produce identical cache keys,
// preventing variant explosion from client-specific quality annotations.
//
// Example: "gzip;q=0.8, br;q=1.0, deflate" → "br,deflate,gzip"
func normalizeAcceptEncoding(val string) string {
	if val == "" {
		return val
	}

	tokens := strings.Split(val, ",")

	// Pre-scan: when the value contains no ';' the per-token Index call is
	// wasted work (the overwhelmingly common case for Accept-Encoding).
	hasQF := strings.Contains(val, ";")
	for i, token := range tokens {
		if hasQF {
			if idx := strings.Index(token, ";"); idx >= 0 {
				tokens[i] = strings.TrimSpace(token[:idx])
				continue
			}
		}
		tokens[i] = strings.TrimSpace(token)
	}

	// Remove identity — it's implied and not a real encoding variant
	tokens = slices.DeleteFunc(tokens, func(s string) bool {
		return s == "" || strings.EqualFold(s, "identity")
	})
	sort.Strings(tokens)
	return strings.Join(tokens, ",")
}
