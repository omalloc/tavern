package http

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	stdhttp "net/http"
	"sort"
	"strings"
)

// HeaderFingerprint returns a deterministic digest of an HTTP header map.
// It is intended for request collapsing, where requests with different
// representation-affecting headers must never share an upstream response.
func HeaderFingerprint(header stdhttp.Header) string {
	type entry struct {
		name   string
		values []string
	}

	grouped := make(map[string][]entry, len(header))
	for name, values := range header {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		grouped[lowerName] = append(grouped[lowerName], entry{
			name:   name,
			values: values,
		})
	}

	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	digest := sha256.New()
	writeSize := func(value uint64) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], value)
		_, _ = digest.Write(size[:])
	}
	writePart := func(value string) {
		writeSize(uint64(len(value)))
		_, _ = digest.Write([]byte(value))
	}

	writeSize(uint64(len(names)))
	for _, name := range names {
		writePart(name)

		entries := grouped[name]
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].name < entries[j].name
		})
		valueCount := 0
		for _, item := range entries {
			valueCount += len(item.values)
		}
		writeSize(uint64(valueCount))
		for _, item := range entries {
			for _, value := range item.values {
				writePart(value)
			}
		}
	}

	return hex.EncodeToString(digest.Sum(nil))
}
