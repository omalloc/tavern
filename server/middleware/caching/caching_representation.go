package caching

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/storage/representation"
)

// Internal staged integration with read-only behavior by default. Its write
// path has no YAML/JSON exposure and never uses the legacy body writer.
type representationLookup struct {
	repository     *representation.IndexRepository
	namespace      string
	isolationScope string
	writes         bool // internal P1.4 admission path, never exposed to config
}

func (c *Caching) lookupRepresentation(store storage.Storage) error {
	c.cacheStatus, c.bypass = storage.BYPASS, true
	c.representationBuckets = store.Buckets()
	if requestBypassesCache(c.req) || hasClientConditions(c.req) || (c.req.Body != nil && c.req.Body != http.NoBody) {
		return nil
	}
	config := c.opt.representation
	if config.repository == nil {
		return storage.ErrControlUnsupported
	}
	key, err := c.representationKey(c.req)
	if err != nil {
		return err
	}
	selected, err := config.repository.Lookup(c.req.Context(), key, representationSelectionHeader(c.req))
	if selected != nil {
		c.resourceMD = selected.Resource
	}
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !selected.Fresh && !config.writes {
		return nil
	}
	received := time.Unix(selected.Representation.RespUnix, 0)
	if _, err := representationFreshness(selected.Representation.Headers.Clone(), received, received); err != nil {
		return nil
	}
	// Resolve the location recorded by the resource, not a newly selected
	// bucket for the representation's hash or a dynamically healthy fallback.
	for _, bucket := range c.representationBuckets {
		if bucket.ID() == selected.Resource.Resource.ActiveLocation {
			c.bucket = bucket
			c.md = selected.Representation
			ref := selected.Reference.Clone()
			c.variantRef = &ref
			c.id = c.md.ID.Clone()
			c.hit, c.bypass = selected.Fresh, !selected.Fresh
			if c.hit {
				c.cacheStatus = storage.CacheHit
			}
			return nil
		}
	}
	return nil
}

func (c *Caching) decorateRepresentation(resp *http.Response) {
	now := time.Now().Unix()
	if c.opt.representation != nil && c.opt.representation.repository != nil {
		now = c.opt.representation.repository.Now().Unix()
	}
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
