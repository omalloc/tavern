package transform

import (
	"net/http"
	"strings"
)

// EncodingAccepted checks the actual original/identity representation. Image
// conversion never invents a gzip/br variant merely by changing response MIME.
func EncodingAccepted(header http.Header, coding string) bool {
	if coding == "" {
		coding = "identity"
	}
	coding = strings.ToLower(coding)
	values := header.Values("Accept-Encoding")
	if len(values) == 0 {
		return true
	}
	raw := strings.Join(values, ",")
	if len(raw) > 16384 || strings.Count(raw, ",") >= 128 {
		return false
	}
	explicit, wildcard := map[string]int{}, -1
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pieces := strings.Split(part, ";")
		if len(pieces) > 2 {
			return false
		}
		name := strings.ToLower(strings.TrimSpace(pieces[0]))
		if name == "" {
			return false
		}
		quality := 1000
		if len(pieces) == 2 {
			key, value, ok := strings.Cut(strings.TrimSpace(pieces[1]), "=")
			if !ok || !strings.EqualFold(key, "q") {
				return false
			}
			var err error
			quality, err = parseQuality(value)
			if err != nil {
				return false
			}
		}
		if name == "*" {
			if wildcard < 0 || quality < wildcard {
				wildcard = quality
			}
			continue
		}
		if previous, ok := explicit[name]; !ok || quality < previous {
			explicit[name] = quality
		}
	}
	if quality, ok := explicit[coding]; ok {
		return quality > 0
	}
	if coding == "identity" {
		return wildcard != 0
	}
	return wildcard > 0
}
