package caching

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kelindar/bitmap"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var (
	ErrVarySizeLimited = errors.New("vary size exceed limit")
	ErrVaryWildcard    = errors.New("vary wildcard response is not reusable")
)

var _ Processor = (*VaryProcessor)(nil)

type VaryOption func(r *VaryProcessor)

type VaryProcessor struct {
	maxLimit      int
	varyIgnoreKey map[string]struct{}
}

func (v *VaryProcessor) Lookup(c *Caching, req *http.Request) (bool, error) {
	if c.hasNoCache() {
		return false, nil
	}
	if !c.md.IsVary() {
		return true, nil
	}

	rawValues := c.md.Headers.Values("Vary")
	rawVary := varycontrol.Clean(rawValues...)
	varyKey := v.filter(rawVary)
	if rawVary.HasWildcard() || len(varyKey) == 0 {
		v.discardRoot(c, "invalid or fully ignored Vary index")
		c.md = nil
		c.rootmd = nil
		c.id = c.cacheRootID()
		return false, nil
	}

	varyData := varyKey.VaryData(req.Header)
	vid, err := newObjectIDFromRequest(req, varyData, c.opt.IncludeQueryInCacheKey)
	if err != nil {
		return false, err
	}

	vmd, lookupErr := c.bucket.Lookup(req.Context(), vid)
	if lookupErr != nil && !errors.Is(lookupErr, storagev1.ErrKeyNotFound) {
		return false, lookupErr
	}

	// Fall back to the pre-v2 key so existing cache entries remain readable.
	if vmd == nil {
		legacyKey := v.filterLegacy(varycontrol.LegacyClean(rawValues...))
		legacyData := legacyKey.LegacyVaryData(req.Header)
		legacyID, legacyErr := newObjectIDFromRequest(req, legacyData, c.opt.IncludeQueryInCacheKey)
		if legacyErr != nil {
			return false, legacyErr
		}
		if legacyID.HashStr() != vid.HashStr() {
			vmd, lookupErr = c.bucket.Lookup(req.Context(), legacyID)
			if lookupErr != nil && !errors.Is(lookupErr, storagev1.ErrKeyNotFound) {
				return false, lookupErr
			}
		}
	}

	if vmd == nil {
		// A known Vary index lets request collapsing isolate this missing
		// representation before the origin response arrives.
		c.id = vid
		return false, nil
	}

	c.rootmd = c.md
	c.id = vmd.ID
	c.md = vmd
	return true, nil
}

func (v *VaryProcessor) PreRequest(_ *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

func (v *VaryProcessor) PostRequest(c *Caching, _ *http.Request, resp *http.Response) (*http.Response, error) {
	rawResponseVary := varycontrol.Clean(resp.Header.Values("Vary")...)
	if rawResponseVary.HasWildcard() {
		c.cacheable = false
		if resp.StatusCode != http.StatusNotModified {
			v.discardRoot(c, ErrVaryWildcard.Error())
		}
		return resp, nil
	}

	responseVary := v.filter(rawResponseVary)
	var err error

	switch {
	case c.md.IsVaryCache():
		err = v.handleCachedVariant(c, resp, responseVary)
	case c.md.IsVary():
		err = v.handleVaryIndex(c, resp, responseVary)
	case len(responseVary) > 0:
		if c.md.Chunks.Count() > 0 {
			v.discardRoot(c, "upgrading cache to Vary structure")
			c.md.Chunks.Clear()
			c.md.Parts.Clear()
		}
		err = v.createVariant(c, responseVary)
	}

	if err != nil {
		c.log.Errorf("Vary processing failed: %v", err)
		c.cacheable = false
	}
	return resp, nil
}

func (v *VaryProcessor) handleCachedVariant(c *Caching, resp *http.Response, responseVary varycontrol.Key) error {
	storedVary := v.storedVary(c)

	// A 304 response commonly omits representation metadata. Keep serving
	// the selected stored response; a non-empty changed Vary is logged and
	// handled on the next full representation response.
	if resp.StatusCode == http.StatusNotModified {
		if len(responseVary) > 0 && !storedVary.Compare(responseVary) {
			c.log.Warnf("origin changed Vary on a 304 response; retaining stored selection fields")
		}
		return nil
	}

	// Partial responses are allowed to omit Vary in practice. The root
	// selection fields remain authoritative for range filling.
	if resp.StatusCode == http.StatusPartialContent && len(responseVary) == 0 {
		return nil
	}

	if len(responseVary) == 0 {
		return v.downgradeToNormal(c, "origin removed Vary")
	}
	if storedVary.Compare(responseVary) {
		return nil
	}

	return v.rebuildVary(c, responseVary, "origin changed Vary")
}

func (v *VaryProcessor) handleVaryIndex(c *Caching, resp *http.Response, responseVary varycontrol.Key) error {
	storedVary := v.filter(varycontrol.Clean(c.md.Headers.Values("Vary")...))
	if len(responseVary) == 0 {
		if resp.StatusCode == http.StatusPartialContent {
			responseVary = storedVary
		} else {
			return v.downgradeToNormal(c, "origin removed Vary")
		}
	}

	if !storedVary.Compare(responseVary) {
		return v.rebuildVary(c, responseVary, "origin changed Vary")
	}
	return v.createVariant(c, responseVary)
}

func (v *VaryProcessor) rebuildVary(c *Caching, responseVary varycontrol.Key, reason string) error {
	v.discardRoot(c, reason)
	v.resetToRoot(c)
	return v.createVariant(c, responseVary)
}

func (v *VaryProcessor) downgradeToNormal(c *Caching, reason string) error {
	v.discardRoot(c, reason)
	v.resetToRoot(c)
	return nil
}

func (v *VaryProcessor) resetToRoot(c *Caching) {
	rootID := c.cacheRootID()
	metadata := c.md.Clone()
	metadata.ID = rootID
	metadata.Flags = object.FlagCache
	metadata.VirtualKey = nil
	metadata.Chunks = bitmap.Bitmap{}
	metadata.Parts = bitmap.Bitmap{}
	metadata.Headers.Del("Vary")

	c.id = rootID
	c.md = metadata
	c.rootmd = nil
}

func (v *VaryProcessor) createVariant(c *Caching, varyKey varycontrol.Key) error {
	varyData := varyKey.VaryData(c.req.Header)
	virtualKeys, err := appendVariantKey(c.md.VirtualKey, varyData, v.maxLimit)
	if err != nil {
		return err
	}

	variantID, err := newObjectIDFromRequest(c.req, varyData, c.opt.IncludeQueryInCacheKey)
	if err != nil {
		return fmt.Errorf("create Vary object ID: %w", err)
	}

	root := c.md.Clone()
	root.ID = c.cacheRootID()
	root.Size = 0
	root.Flags = object.FlagVaryIndex
	root.Chunks = bitmap.Bitmap{}
	root.Parts = bitmap.Bitmap{}
	root.VirtualKey = virtualKeys
	root.Headers = http.Header{"Vary": append([]string(nil), varyKey...)}

	headers := c.md.Headers.Clone()
	headers.Del("Vary")
	for _, key := range varyKey {
		headers.Add("Vary", key)
	}

	now := time.Now().Unix()
	variant := &object.Metadata{
		ID:          variantID,
		RespUnix:    c.md.RespUnix,
		LastRefUnix: now,
		Code:        c.md.Code,
		Size:        c.md.Size,
		BlockSize:   c.md.BlockSize,
		Chunks:      bitmap.Bitmap{},
		Parts:       bitmap.Bitmap{},
		Headers:     headers,
		ExpiresAt:   c.md.ExpiresAt,
		Flags:       object.FlagVaryCache,
	}

	c.rootmd = root
	c.md = variant
	c.id = variantID
	return nil
}

func (v *VaryProcessor) storedVary(c *Caching) varycontrol.Key {
	if c.rootmd != nil {
		return v.filter(varycontrol.Clean(c.rootmd.Headers.Values("Vary")...))
	}
	return v.filter(varycontrol.Clean(c.md.Headers.Values("Vary")...))
}

func (v *VaryProcessor) filter(keys varycontrol.Key) varycontrol.Key {
	return slices.DeleteFunc(keys, func(key string) bool {
		_, ignored := v.varyIgnoreKey[strings.ToLower(key)]
		return ignored
	})
}

func (v *VaryProcessor) filterLegacy(keys varycontrol.Key) varycontrol.Key {
	return slices.DeleteFunc(keys, func(key string) bool {
		_, ignored := v.varyIgnoreKey[strings.ToLower(key)]
		return ignored
	})
}

func (v *VaryProcessor) discardRoot(c *Caching, reason string) {
	rootID := c.cacheRootID()
	if rootID == nil || c.bucket == nil {
		return
	}
	if err := c.bucket.DiscardWithMessage(c.req.Context(), rootID, reason); err != nil &&
		!errors.Is(err, storagev1.ErrKeyNotFound) && !os.IsNotExist(err) {
		c.log.Errorf("discard Vary root failed: %v", err)
	}

	// Old or damaged root indexes may not list the active child. Remove it
	// explicitly as well so a rebuild cannot leave that variant orphaned.
	if c.id != nil && c.id.HashStr() != rootID.HashStr() {
		if err := c.bucket.DiscardWithMessage(c.req.Context(), c.id, reason); err != nil &&
			!errors.Is(err, storagev1.ErrKeyNotFound) && !os.IsNotExist(err) {
			c.log.Errorf("discard active Vary variant failed: %v", err)
		}
	}
}

func appendVariantKey(existing []string, key string, limit int) ([]string, error) {
	keys := make([]string, 0, len(existing)+1)
	for _, item := range existing {
		if !slices.Contains(keys, item) {
			keys = append(keys, item)
		}
	}
	if slices.Contains(keys, key) {
		return keys, nil
	}
	if limit <= 0 || len(keys) >= limit {
		return nil, ErrVarySizeLimited
	}
	return append(keys, key), nil
}

func (c *Caching) cacheRootID() *object.ID {
	if c.rootID != nil {
		return c.rootID
	}
	if c.rootmd != nil && c.rootmd.ID != nil {
		c.rootID = c.rootmd.ID
		return c.rootID
	}
	if c.md != nil && c.md.IsVary() && c.md.ID != nil {
		c.rootID = c.md.ID
		return c.rootID
	}
	if c.id != nil {
		c.rootID = object.NewVirtualID(c.id.Path(), "")
	}
	return c.rootID
}

// storeRootMetadata serializes read/merge/write of the root Vary index inside
// this process. This prevents concurrent representation misses from losing
// each other's VirtualKey entries.
func (c *Caching) storeRootMetadata() error {
	root := c.rootmd
	if root == nil || root.ID == nil {
		return nil
	}

	lock := globalLocker.getLock(root.ID.HashStr())
	lock.Lock()
	defer lock.Unlock()

	latest, err := c.bucket.Lookup(c.req.Context(), root.ID)
	if err != nil && !errors.Is(err, storagev1.ErrKeyNotFound) && !os.IsNotExist(err) {
		return err
	}
	if latest != nil && latest.IsVary() {
		latestVary := varycontrol.Clean(latest.Headers.Values("Vary")...)
		rootVary := varycontrol.Clean(root.Headers.Values("Vary")...)
		if latestVary.Compare(rootVary) {
			merged := append([]string(nil), latest.VirtualKey...)
			for _, key := range root.VirtualKey {
				merged, err = appendVariantKey(merged, key, c.opt.VaryLimit)
				if err != nil {
					return err
				}
			}
			root.VirtualKey = merged
		}
	}

	return c.bucket.Store(c.req.Context(), root)
}

func NewVaryProcessor(opts ...VaryOption) *VaryProcessor {
	v := &VaryProcessor{
		maxLimit:      100,
		varyIgnoreKey: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

func WithVaryMaxLimit(limit int) VaryOption {
	return func(r *VaryProcessor) {
		r.maxLimit = limit
	}
}

func WithVaryIgnoreKeys(keys ...string) VaryOption {
	return func(r *VaryProcessor) {
		for _, key := range keys {
			r.varyIgnoreKey[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
		}
	}
}
