// Package transform normalizes managed image decisions without performing I/O.
package transform

import (
	"errors"
	"mime"
	"net/http"
	"sort"
	"strings"
)

var (
	ErrInvalidAccept    = errors.New("invalid Accept field")
	ErrNotAcceptable    = errors.New("no acceptable image representation")
	ErrInvalidParameter = errors.New("invalid transform parameter")
)

const (
	maxAcceptBytes = 16 * 1024
	maxMediaRanges = 128
)

type mediaRange struct {
	kind, subtype string
	params        map[string]string
	q             int
}
type Accept struct {
	missing bool
	ranges  []mediaRange
}

// ParseAccept follows RFC 9110 12.4.2/12.5.1. Malformed or over-budget managed
// negotiation fails explicitly; it never turns a parse failure into */*.
func ParseAccept(header http.Header) (Accept, error) {
	keys := make([]string, 0, 1)
	for key := range header {
		if strings.EqualFold(key, "Accept") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	result := Accept{missing: len(keys) == 0}
	size := 0
	for _, key := range keys {
		for _, line := range header[key] {
			size += len(line) + 1
			if size > maxAcceptBytes {
				return Accept{}, ErrInvalidAccept
			}
			items, err := splitQuoted(line, ',')
			if err != nil {
				return Accept{}, err
			}
			for _, item := range items {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				if len(result.ranges) >= maxMediaRanges {
					return Accept{}, ErrInvalidAccept
				}
				media, params, err := mime.ParseMediaType(item)
				if err != nil {
					return Accept{}, ErrInvalidAccept
				}
				kind, subtype, ok := strings.Cut(media, "/")
				if !ok || kind == "" || subtype == "" || kind == "*" && subtype != "*" || strings.Contains(kind, "*") && kind != "*" || strings.Contains(subtype, "*") && subtype != "*" {
					return Accept{}, ErrInvalidAccept
				}
				q := 1000
				if raw, present := params["q"]; present {
					sections, err := splitQuoted(item, ';')
					if err != nil {
						return Accept{}, err
					}
					count := 0
					for _, section := range sections[1:] {
						name, value, ok := strings.Cut(section, "=")
						if ok && strings.EqualFold(strings.TrimSpace(name), "q") {
							count++
							if strings.HasPrefix(strings.TrimSpace(value), "\"") {
								return Accept{}, ErrInvalidAccept
							}
						}
					}
					if count != 1 {
						return Accept{}, ErrInvalidAccept
					}
					q, err = parseQuality(raw)
					if err != nil {
						return Accept{}, err
					}
					delete(params, "q")
				}
				result.ranges = append(result.ranges, mediaRange{kind, subtype, params, q})
			}
		}
	}
	return result, nil
}

func splitQuoted(value string, separator byte) ([]string, error) {
	var items []string
	start := 0
	quoted, escape := false, false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escape {
			escape = false
			continue
		}
		if quoted && c == '\\' {
			escape = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if c == separator && !quoted {
			items = append(items, value[start:i])
			start = i + 1
		}
	}
	if quoted || escape {
		return nil, ErrInvalidAccept
	}
	return append(items, value[start:]), nil
}
func parseQuality(raw string) (int, error) {
	if raw == "0" {
		return 0, nil
	}
	if raw == "1" {
		return 1000, nil
	}
	if len(raw) < 2 || len(raw) > 5 || raw[1] != '.' || (raw[0] != '0' && raw[0] != '1') {
		return 0, ErrInvalidAccept
	}
	result := 0
	for i := 2; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' || raw[0] == '1' && raw[i] != '0' {
			return 0, ErrInvalidAccept
		}
		result = result*10 + int(raw[i]-'0')
	}
	for i := len(raw) - 2; i < 3; i++ {
		result *= 10
	}
	if raw[0] == '1' {
		return 1000, nil
	}
	return result, nil
}

// Quality first selects the most specific matching media range, including its
// media parameters. Equal-specificity duplicates use the lower weight, making
// conflicting exclusions deterministic and conservative across field order.
func (a Accept) Quality(mediaType string) int {
	media, params, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return 0
	}
	kind, subtype, ok := strings.Cut(media, "/")
	if !ok {
		return 0
	}
	if a.missing {
		return 1000
	}
	bestKind, bestParams, quality := -1, -1, 0
	for _, r := range a.ranges {
		if r.kind != "*" && r.kind != kind || r.subtype != "*" && r.subtype != subtype {
			continue
		}
		matched := true
		for name, value := range r.params {
			if actual, ok := params[name]; !ok || actual != value {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		specificity := 2
		if r.kind == "*" {
			specificity = 0
		} else if r.subtype == "*" {
			specificity = 1
		}
		if specificity > bestKind || specificity == bestKind && len(r.params) > bestParams {
			bestKind, bestParams, quality = specificity, len(r.params), r.q
		} else if specificity == bestKind && len(r.params) == bestParams {
			quality = min(quality, r.q)
		}
	}
	return quality
}
func (a Accept) preferSource() bool {
	if a.missing {
		return true
	}
	for _, r := range a.ranges {
		if r.kind == "image" {
			return false
		}
	}
	return len(a.ranges) > 0
}

func FormatMIME(format string) (string, error) {
	switch strings.ToLower(format) {
	case "jpeg", "jpg", "image/jpeg":
		return "image/jpeg", nil
	case "png", "image/png":
		return "image/png", nil
	case "webp", "image/webp":
		return "image/webp", nil
	case "source":
		return "source", nil
	default:
		return "", ErrInvalidParameter
	}
}

// Choose returns one actual MIME. Configured order breaks weight ties; input
// field ordering never does. Missing and generic */* preferences preserve source.
func (a Accept) Choose(source string, supported []string, explicit string) (string, error) {
	if !knownMIME(source) {
		return "", ErrInvalidParameter
	}
	for _, candidate := range supported {
		if !knownMIME(candidate) {
			return "", ErrInvalidParameter
		}
	}
	if explicit != "" {
		selected, err := FormatMIME(explicit)
		if err != nil {
			return "", err
		}
		if selected == "source" {
			selected = source
		}
		allowed := selected == source
		for _, candidate := range supported {
			if candidate == selected {
				allowed = true
			}
		}
		if !allowed || a.Quality(selected) == 0 {
			return "", ErrNotAcceptable
		}
		return selected, nil
	}
	if a.preferSource() && a.Quality(source) > 0 {
		return source, nil
	}
	selected := ""
	quality := 0
	for _, candidate := range append(append([]string(nil), supported...), source) {
		if !knownMIME(candidate) {
			return "", ErrInvalidParameter
		}
		q := a.Quality(candidate)
		if q > quality {
			selected, quality = candidate, q
		}
	}
	if selected == "" {
		return "", ErrNotAcceptable
	}
	return selected, nil
}
func knownMIME(value string) bool {
	return value == "image/jpeg" || value == "image/png" || value == "image/webp"
}
