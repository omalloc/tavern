package caching

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/omalloc/tavern/storage/representation"
)

// Runtime binding for opt-in v2 representations; default configuration remains
// disabled. V2 publication never uses the legacy body writer.
type representationLookup struct {
	repository     *representation.IndexRepository
	manager        *representation.Manager
	namespace      string
	isolationScope string
	writes         bool // resolved by validated runtime configuration
}

func (c *Caching) lookupRepresentation(store storage.Storage) (failure error) {
	result, reason := "miss", "not_found"
	defer func() {
		if failure != nil {
			result, reason = "bypass", "control_error"
		}
		pkgmetrics.RecordVaryLookup(result, reason)
	}()
	c.cacheStatus, c.bypass = storage.BYPASS, true
	c.representationBuckets = store.Buckets()
	if provider, ok := store.(storage.RepresentationBucketProvider); ok {
		c.representationBuckets = provider.RepresentationBuckets()
	}
	switch {
	case requestBypassesCache(c.req):
		result, reason = "bypass", "request_policy"
		return nil
	case hasClientConditions(c.req):
		result, reason = "bypass", "conditions"
		return nil
	case c.req.Body != nil && c.req.Body != http.NoBody:
		result, reason = "bypass", "body"
		return nil
	}
	config := c.opt.representation
	repo, err := c.resolveRepresentationRepository(c.req)
	if err != nil {
		return err
	}
	key, err := c.representationKey(c.req)
	if err != nil {
		return err
	}
	selected, err := repo.LookupRead(c.req.Context(), key, representationSelectionHeader(c.req))
	defer func() {
		if selected != nil && selected.Lease != nil && c.representationLease != selected.Lease {
			selected.Lease.Close()
		}
	}()
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
		reason = "stale"
	}
	if !selected.Fresh && !config.writes {
		return nil
	}
	received := time.Unix(selected.Representation.RespUnix, 0)
	if _, err := representationFreshness(selected.Representation.Headers.Clone(), received, received); err != nil {
		result, reason = "bypass", "policy"
		return nil
	}
	// Resolve the location recorded by the resource, not a newly selected
	// bucket for the representation's hash or a dynamically healthy fallback.
	for _, bucket := range c.representationBuckets {
		if bucket.ID() == selected.Resource.Resource.ActiveLocation {
			c.bucket = bucket
			c.representationLease = selected.Lease
			c.md = selected.Representation
			ref := selected.Reference.Clone()
			c.variantRef = &ref
			c.id = c.md.ID.Clone()
			rng, rangeErr := representationRequestRange(c.req.Header.Get("Range"), c.md.Size)
			readable := rangeErr == nil && c.md.HasReadableRange(uint64(rng.Start), uint64(rng.End))
			// HEAD needs no bytes. Multi-range/invalid requests stay with the
			// origin rather than silently selecting only their first range.
			if c.req.Method == http.MethodHead {
				readable = c.md.HasComplete()
			}
			if strings.Contains(c.req.Header.Get("Range"), ",") {
				readable = false
			}
			c.hit = selected.Fresh && readable
			if selected.Fresh && !readable {
				reason = "range"
			}
			c.bypass = !c.hit
			if c.hit {
				result, reason = "hit", "matched"
				c.cacheStatus = storage.CacheHit
				now := repo.Now().Unix()
				if c.opt.FuzzyRefresh && c.opt.FuzzyRefreshRate > 0 && c.md.HasComplete() && hasConditionHeader(c.md.Headers) &&
					shouldTriggerFuzzyRefresh(now, calculateSoftTTL(c.md.RespUnix, c.md.ExpiresAt, c.opt.FuzzyRefreshRate), c.md.ExpiresAt) {
					background := c.cloneForBackground(c.req)
					go (&RevalidateProcessor{}).asyncRevalidate(background, background.req)
				}
			}
			return nil
		}
	}
	return nil
}

func (c *Caching) decorateRepresentation(resp *http.Response) {
	now := time.Now().Unix()
	if c.representationRepo != nil {
		now = c.representationRepo.Now().Unix()
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

func (c *Caching) resolveRepresentationRepository(req *http.Request) (*representation.IndexRepository, error) {
	if c.representationRepo != nil {
		return c.representationRepo, nil
	}
	config := c.opt.representation
	if config.repository != nil {
		c.representationRepo = config.repository
		return c.representationRepo, nil
	}
	if config.manager != nil {
		key, err := c.representationKey(req)
		if err != nil {
			return nil, err
		}
		repo, err := config.manager.Repository(key)
		if err != nil {
			return nil, err
		}
		c.representationRepo = repo
		return repo, nil
	}
	return nil, storage.ErrControlUnsupported
}
