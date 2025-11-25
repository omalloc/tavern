package middleware

import "github.com/omalloc/tavern/pkg/mapstruct"

// Middleware represents a middleware configuration.
type Middleware struct {
	Name     string         `json:"name" yaml:"name"`
	Required bool           `json:"required,omitempty" yaml:"required,omitempty"`
	Options  map[string]any `json:"options,omitempty" yaml:"options,omitempty"`
}

// Unmarshal decodes the input into the Options map.
func (m *Middleware) Unmarshal(in any) error {
	if m.Options == nil {
		return nil
	}
	return mapstruct.Decode(m.Options, in)
}
