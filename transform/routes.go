package transform

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

type Route struct {
	FallbackToSource bool               `json:"fallback_to_source" yaml:"fallback_to_source"`
	PathPrefix       string             `json:"path_prefix" yaml:"path_prefix"`
	Mode             string             `json:"mode" yaml:"mode"`
	FormatParameter  string             `json:"format_parameter" yaml:"format_parameter"`
	QualityParameter string             `json:"quality_parameter" yaml:"quality_parameter"`
	Policy           transformv1.Policy `json:"policy" yaml:"policy"`
}

type Router struct{ routes []Route }

func NewRouter(routes []Route, caps transformv1.BackendCapabilities) (*Router, error) {
	if err := ValidateCapabilities(caps); err != nil {
		return nil, err
	}
	owned := slices.Clone(routes)
	seen := make(map[string]bool)
	for i, route := range owned {
		path, err := url.ParseRequestURI(route.PathPrefix)
		if err != nil || path.IsAbs() || path.Host != "" || path.RawQuery != "" || path.ForceQuery || !strings.HasPrefix(route.PathPrefix, "/") || strings.HasPrefix(route.PathPrefix, "//") || !strings.HasSuffix(route.PathPrefix, "/") || path.EscapedPath() != route.PathPrefix || seen[route.PathPrefix] {
			return nil, fmt.Errorf("invalid or duplicate managed path prefix: %w", ErrInvalidParameter)
		}
		seen[route.PathPrefix] = true
		if route.Mode == "" {
			route.Mode = "auto"
		}
		if route.Mode != "auto" && route.Mode != "explicit" {
			return nil, ErrInvalidParameter
		}
		if route.FormatParameter == "" {
			route.FormatParameter = "format"
		}
		if route.QualityParameter == "" {
			route.QualityParameter = "quality"
		}
		if !validParameterName(route.FormatParameter) || !validParameterName(route.QualityParameter) || route.FormatParameter == route.QualityParameter {
			return nil, ErrInvalidParameter
		}
		route.Policy, err = normalizePolicy(route.Policy, caps)
		if err != nil {
			return nil, err
		}
		for _, format := range route.Policy.Formats {
			codec, _ := codecFor(caps, format)
			if format == "image/png" {
				if *route.Policy.PNGCompression < codec.CompressionMin || *route.Policy.PNGCompression > codec.CompressionMax {
					return nil, fmt.Errorf("route PNG compression exceeds backend bounds: %w", ErrInvalidParameter)
				}
			} else if route.Policy.Quality < codec.QualityMin || route.Policy.Quality > codec.QualityMax {
				return nil, fmt.Errorf("route quality exceeds backend bounds: %w", ErrInvalidParameter)
			}
			if format == "image/jpeg" && route.Policy.AlphaPolicy == "flatten" && !caps.FlattenAlpha {
				return nil, ErrAlphaLoss
			}
		}
		owned[i] = route
	}
	// Longest escaped prefix wins; membership is fixed and independent of health.
	sort.Slice(owned, func(i, j int) bool { return len(owned[i].PathPrefix) > len(owned[j].PathPrefix) })
	return &Router{routes: owned}, nil
}
func validParameterName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Resolve maps a configured public URL to its same-origin source. It does not
// infer aliases from file extensions or rewrite unrelated raw query bytes.
// PURGE routing uses this same resolver; there is no second query parser.
func (r *Router) Resolve(req *http.Request) (Plan, bool, error) {
	return r.resolve(req, false)
}

// ResolvePurge uses the same parameter mapping without requiring a target
// format: invalidation also accepts source URLs and directory prefixes.
func (r *Router) ResolvePurge(req *http.Request) (Plan, bool, error) {
	return r.resolve(req, true)
}

func (r *Router) resolve(req *http.Request, purge bool) (Plan, bool, error) {
	for _, route := range r.routes {
		if !strings.HasPrefix(req.URL.EscapedPath(), route.PathPrefix) {
			continue
		}
		options, query, forceQuery, err := mapParameters(req.URL.RawQuery, req.URL.ForceQuery, route)
		if err != nil {
			return Plan{}, true, err
		}
		if !purge && route.Mode == "explicit" && options.Format == "" {
			return Plan{}, true, ErrInvalidParameter
		}
		source := *req.URL
		source.RawQuery, source.ForceQuery = query, forceQuery
		if source.Scheme == "" {
			source.Scheme = "http"
			if req.TLS != nil {
				source.Scheme = "https"
			}
		}
		if req.Host != "" {
			source.Host = req.Host
		}
		key, err := object.NewResourceKey(source.String(), "managed-route", "")
		if err != nil {
			return Plan{}, true, ErrInvalidParameter
		}
		if !purge {
			if _, err = ParseAccept(ClientHeaders(req)); err != nil {
				return Plan{}, true, err
			}
		}
		return clonePlan(Plan{SourceURL: key.CanonicalURL(), FallbackToSource: route.FallbackToSource, Policy: route.Policy, Options: options, AcceptHeader: ClientHeaders(req)}), true, nil
	}
	return Plan{}, false, nil
}

func mapParameters(raw string, force bool, route Route) (transformv1.RequestOptions, string, bool, error) {
	var options transformv1.RequestOptions
	if len(raw) > 16*1024 {
		return options, "", false, ErrInvalidParameter
	}
	if raw == "" {
		return options, "", force, nil
	}
	retained := make([]string, 0)
	seen := make(map[string]bool)
	for _, field := range strings.Split(raw, "&") {
		if strings.Contains(field, ";") {
			return options, "", false, ErrInvalidParameter
		}
		rawName, rawValue, _ := strings.Cut(field, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			return options, "", false, ErrInvalidParameter
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return options, "", false, ErrInvalidParameter
		}
		if name != route.FormatParameter && name != route.QualityParameter {
			retained = append(retained, field)
			continue
		}
		if seen[name] {
			return options, "", false, ErrInvalidParameter
		}
		seen[name] = true
		if name == route.FormatParameter {
			output, err := FormatMIME(value)
			if err != nil {
				return options, "", false, err
			}
			options.Format = output
		} else {
			if len(value) == 0 || len(value) > 3 {
				return options, "", false, ErrInvalidParameter
			}
			for _, c := range value {
				if c < '0' || c > '9' {
					return options, "", false, ErrInvalidParameter
				}
			}
			quality, err := strconv.Atoi(value)
			if err != nil || quality < 1 || quality > 100 {
				return options, "", false, ErrInvalidParameter
			}
			options.Quality = &quality
		}
	}
	if (options.Format == "source" || options.Format == "image/png") && options.Quality != nil {
		return options, "", false, ErrInvalidParameter
	}
	query := strings.Join(retained, "&")
	return options, query, len(retained) > 0 && query == "", nil
}
