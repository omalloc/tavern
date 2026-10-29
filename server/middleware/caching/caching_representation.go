package caching

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/internal/protocol"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/storage/representation"
)

// Read-only integration for the controlled P1.3 path. It has no YAML/JSON
// exposure and cannot publish an origin response through the legacy writer.
type representationLookup struct {
	repository     *representation.IndexRepository
	namespace      string
	isolationScope string
}

func (c *Caching) lookupRepresentation(store storage.Storage) error {
	c.cacheStatus, c.bypass = storage.BYPASS, true
	if requestBypassesCache(c.req) {
		return nil
	}
	// Conditional response assembly and validation updates belong to P1.4.
	for _, field := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range"} {
		if _, present := c.req.Header[field]; present {
			return nil
		}
	}
	config := c.opt.representation
	if config.repository == nil {
		return storage.ErrControlUnsupported
	}
	u := *c.req.URL
	if u.Host == "" {
		u.Host = c.req.Host
	}
	if u.Scheme == "" {
		u.Scheme = xhttp.Scheme(c.req)
	}
	if !c.opt.IncludeQueryInCacheKey {
		u.RawQuery, u.ForceQuery = "", false
	}
	key, err := object.NewResourceKey(u.String(), config.namespace, config.isolationScope)
	if err != nil {
		return err
	}
	selected, err := config.repository.Lookup(c.req.Context(), key, c.req.Header)
	if selected != nil {
		c.resourceMD = selected.Resource
	}
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !selected.Fresh {
		return nil
	}
	if _, allowed := xhttp.ParseCacheTime(protocol.ProtocolCacheTime, selected.Representation.Headers); !allowed {
		return nil
	}
	// Resolve the location recorded by the resource, not a newly selected
	// bucket for the representation's hash or a dynamically healthy fallback.
	for _, bucket := range store.Buckets() {
		if bucket.ID() == selected.Resource.Resource.ActiveLocation {
			c.bucket = bucket
			c.md = selected.Representation
			ref := selected.Reference.Clone()
			c.variantRef = &ref
			c.id = c.md.ID.Clone()
			c.hit, c.bypass = true, false
			c.cacheStatus = storage.CacheHit
			return nil
		}
	}
	return nil
}

func (c *Caching) decorateRepresentation(resp *http.Response) {
	now := time.Now().Unix()
	resident := max(int64(0), now-c.md.RespUnix)
	age, err := strconv.ParseInt(c.md.Headers.Get("Age"), 10, 64)
	if err != nil || age < 0 {
		age = 0
	}
	if age > math.MaxInt64-resident {
		age = math.MaxInt64
	} else {
		age += resident
	}
	if date, err := http.ParseTime(c.md.Headers.Get("Date")); err == nil {
		age = max(age, now-date.Unix())
	}
	resp.Header.Set("Age", strconv.FormatInt(age, 10))
}
