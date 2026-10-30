package caching

import (
	"net/http"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/cachecontrol"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var _ Processor = (*VaryProcessor)(nil)

type VaryOption func(*VaryProcessor)

type VaryProcessor struct {
	maxLimit int
}

// Lookup never reads the legacy header/VirtualKey index. Those keys lost field
// boundaries and cannot safely distinguish representations. Opt-in v2 lookups
// run before this legacy processor and own their separate lifecycle.
func (v *VaryProcessor) Lookup(c *Caching, _ *http.Request) (bool, error) {
	if c.md == nil {
		return false, nil
	}
	if legacyVary(c.md) {
		c.md = nil
		c.bypass = true
		c.cacheStatus = storage.BYPASS
		return false, nil
	}
	return true, nil
}

func (v *VaryProcessor) PreRequest(_ *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

// Admission happens before body wrapping in doProxy; this processor must never
// create or mutate a legacy Vary index after a writer has been constructed.
func (v *VaryProcessor) PostRequest(_ *Caching, _ *http.Request, resp *http.Response) (*http.Response, error) {
	return resp, nil
}

func legacyVary(md *object.Metadata) bool {
	if md == nil {
		return false
	}
	fields, wildcard, err := varycontrol.Parse(md.Headers.Values("Vary")...)
	return md.IsVary() || md.IsVaryCache() || md.HasVary() || err != nil || wildcard || len(fields) > 0
}

func requestBypassesCache(req *http.Request) bool {
	// Even an empty Authorization field is conservatively treated as an
	// authenticated request. Do not share either reads or writes initially.
	for name := range req.Header {
		if strings.EqualFold(name, "Authorization") {
			return true
		}
	}
	ctrl := cachecontrol.Parse(strings.Join(req.Header.Values("Cache-Control"), ","))
	noCache, _ := ctrl.NoCache()
	return ctrl.NoStore() || noCache || ctrl.MaxAge() == 0 ||
		strings.EqualFold(strings.TrimSpace(req.Header.Get("Pragma")), "no-cache")
}

func admitResponse(req *http.Request, resp *http.Response) bool {
	if requestBypassesCache(req) {
		pkgmetrics.RecordVaryAdmission("bypass", "policy")
		return false
	}
	fields, wildcard, err := varycontrol.Parse(resp.Header.Values("Vary")...)
	if err != nil || wildcard {
		pkgmetrics.RecordVaryAdmission("bypass", varyAdmissionReason(err, wildcard))
		return false
	}
	if len(fields) > 0 {
		pkgmetrics.RecordVaryAdmission("bypass", "legacy")
		return false
	}
	_, cacheable := xhttp.ParseCacheTime(protocol.ProtocolCacheTime, resp.Header)
	if cacheable {
		pkgmetrics.RecordVaryAdmission("admitted", "legacy")
	} else {
		pkgmetrics.RecordVaryAdmission("bypass", "policy")
	}
	return cacheable
}

func NewVaryProcessor(opts ...VaryOption) *VaryProcessor {
	v := &VaryProcessor{maxLimit: 100}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

func WithVaryMaxLimit(limit int) VaryOption {
	return func(v *VaryProcessor) { v.maxLimit = limit }
}

// WithVaryIgnoreKeys is retained for configuration compatibility.
// Deprecated: ignoring arbitrary selection fields is unsafe and has no effect.
func WithVaryIgnoreKeys(_ ...string) VaryOption {
	return func(_ *VaryProcessor) {}
}
