// Package varycontrol implements conservative HTTP Vary selection semantics.
package varycontrol

import (
	"errors"
	"slices"
	"strings"
)

const MaxFields = 32

var (
	ErrInvalidField  = errors.New("invalid Vary field name")
	ErrTooManyFields = errors.New("too many Vary fields")
)

type Key []string

// Parse reads field names, not field values. It normalizes names to lowercase
// and returns a sorted, unique set. A wildcard anywhere prevents reuse, even
// when other fields are also present. Empty list members are ignored.
func Parse(values ...string) (fields Key, wildcard bool, err error) {
	seen := make(map[string]struct{})
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			name := strings.Trim(item, " \t")
			if name == "" {
				continue
			}
			if name == "*" {
				wildcard = true
				continue
			}
			if !validField(name) {
				return nil, wildcard, ErrInvalidField
			}
			name = strings.ToLower(name)
			seen[name] = struct{}{}
			if len(seen) > MaxFields {
				return nil, wildcard, ErrTooManyFields
			}
		}
	}
	for name := range seen {
		fields = append(fields, name)
	}
	slices.Sort(fields)
	return fields, wildcard, nil
}

func validField(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return name != ""
}
