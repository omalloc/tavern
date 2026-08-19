package caching

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kelindar/bitmap"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/pkg/x/http/varycontrol"
)

var (
	ErrHeaderNoMatchVaryKey  = errors.New("header no match vary key")
	ErrHeaderNoMatchVaryData = errors.New("header no match vary data")
	ErrVarySizeLimited       = errors.New("vary size exceed limit")
	ErrVaryDowngradeNormal   = errors.New("vary downgrade to normal cache")
)

const variantIndexVersion uint8 = 2

var variantIndexLocks [64]sync.Mutex

var _ Processor = (*VaryProcessor)(nil)

type VaryOption func(*VaryProcessor)

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
	if c.md.VariantIndex == nil || c.md.VariantIndex.Version != variantIndexVersion {
		// v1 is deliberately not guessed into v2. A later miss rebuilds it.
		return false, nil
	}

	type candidate struct {
		ref object.VariantRef
		md  *object.Metadata
	}
	digests := make(map[string]string)
	var matches []object.VariantRef
	for _, ref := range c.md.VariantIndex.Variants {
		digest, ok := digests[ref.Schema.ID]
		if !ok {
			digest = varycontrol.Key(ref.Schema.Fields).SelectorDigest(req.Header)
			digests[ref.Schema.ID] = digest
		}
		if digest == ref.SelectorDigest && len(ref.Dimensions) == 0 {
			matches = append(matches, ref)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if (len(matches[i].Schema.Fields) > 0) != (len(matches[j].Schema.Fields) > 0) {
			return len(matches[i].Schema.Fields) > 0
		}
		if matches[i].ResponseDate != matches[j].ResponseDate {
			return matches[i].ResponseDate > matches[j].ResponseDate
		}
		return matches[i].StoredAtUnix > matches[j].StoredAtUnix
	})

	var found *candidate
	dangling := make(map[string]struct{})
	for _, ref := range matches {
		id := object.NewVirtualID(c.md.ID.Path(), ref.ObjectKey)
		md, err := c.bucket.Lookup(req.Context(), id)
		if err == nil && md != nil {
			found = &candidate{ref: ref, md: md}
			break
		}
		dangling[ref.ObjectKey] = struct{}{}
	}
	if len(dangling) > 0 {
		v.removeDangling(c, dangling)
	}
	if found == nil {
		return false, nil
	}
	c.rootmd = c.md
	c.id = found.md.ID
	c.md = found.md
	return true, nil
}

func (v *VaryProcessor) removeDangling(c *Caching, dangling map[string]struct{}) {
	baseID := c.md.ID
	lock := &variantIndexLocks[int(baseID.Hash()[0])%len(variantIndexLocks)]
	lock.Lock()
	defer lock.Unlock()
	latest, _ := c.bucket.Lookup(context.Background(), baseID)
	if latest == nil || latest.VariantIndex == nil {
		return
	}
	root := latest.Clone()
	root.VariantIndex.Variants = slices.DeleteFunc(root.VariantIndex.Variants, func(ref object.VariantRef) bool {
		_, remove := dangling[ref.ObjectKey]
		return remove
	})
	if len(root.VariantIndex.Variants) == 0 {
		_ = c.bucket.Discard(context.Background(), baseID)
		return
	}
	root.VariantIndex.Revision++
	_ = c.bucket.Store(c.ctx, root)
	c.md = root
}

func (v *VaryProcessor) PreRequest(_ *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

func (v *VaryProcessor) PostRequest(c *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	if c.md.IsVaryCache() {
		return resp, nil
	}

	schemaKey := varycontrol.CleanWithIgnore(v.varyIgnoreKey, resp.Header.Values("Vary")...)
	if schemaKey.Wildcard() {
		c.bypassStore = true
		c.cacheable = false
		return resp, nil
	}

	wasIndex := c.md.IsVary()
	if len(schemaKey) == 0 && !wasIndex {
		return resp, nil
	}
	if req.Method == http.MethodHead || v.maxLimit <= 0 {
		c.bypassStore = true
		c.cacheable = false
		return resp, nil
	}

	baseID := object.NewID(c.md.ID.Path())
	if wasIndex && (c.md.VariantIndex == nil || c.md.VariantIndex.Version != variantIndexVersion) {
		// v1 VirtualKey is ambiguous: recursively delete it and rebuild v2.
		if err := c.bucket.Discard(context.Background(), baseID); err != nil && !os.IsNotExist(err) {
			c.log.Warnf("discard legacy Vary family failed: %v", err)
		}
		wasIndex = false
	}

	digest := schemaKey.SelectorDigest(req.Header)
	schema := object.VarySchema{
		ID:                   schemaKey.ID(),
		Fields:               append([]string(nil), schemaKey...),
		NormalizationVersion: varycontrol.NormalizationVersion,
	}
	objectKey := variantObjectKey(schema.ID, digest, nil)
	childID := object.NewVirtualID(baseID.Path(), objectKey)

	child := c.md.Clone()
	child.ID = childID
	child.Flags = object.FlagVaryCache
	child.VirtualKey = nil
	child.VariantIndex = nil
	child.Variant = &object.VariantMeta{Schema: schema, SelectorDigest: digest}
	child.Chunks = bitmap.Bitmap{}
	child.Parts = bitmap.Bitmap{}

	now := time.Now().Unix()
	ref := object.VariantRef{
		ObjectKey: objectKey, Schema: schema, SelectorDigest: digest,
		StoredAtUnix: now, ResponseDate: child.RespUnix,
	}
	c.id = childID
	c.md = child
	c.rootmd = nil // publication happens after the child reaches EOF.
	c.publishVariant = func(complete bool) {
		if complete {
			v.publish(c, baseID, ref, wasIndex)
		}
	}
	return resp, nil
}

func (v *VaryProcessor) publish(c *Caching, baseID *object.ID, ref object.VariantRef, wasIndex bool) {
	lock := &variantIndexLocks[int(baseID.Hash()[0])%len(variantIndexLocks)]
	lock.Lock()
	defer lock.Unlock()

	latest, _ := c.bucket.Lookup(context.Background(), baseID)
	var root *object.Metadata
	if latest != nil && latest.IsVary() && latest.VariantIndex != nil && latest.VariantIndex.Version == variantIndexVersion {
		root = latest.Clone()
	} else {
		root = &object.Metadata{
			ID: baseID, Flags: object.FlagVaryIndex, BlockSize: c.md.BlockSize,
			Headers: http.Header{}, Chunks: bitmap.Bitmap{}, Parts: bitmap.Bitmap{},
			VariantIndex: &object.VariantIndex{Version: variantIndexVersion},
		}
		if wasIndex && latest != nil {
			root.RespUnix = latest.RespUnix
		}
	}

	replaced := false
	for i := range root.VariantIndex.Variants {
		if root.VariantIndex.Variants[i].ObjectKey == ref.ObjectKey {
			root.VariantIndex.Variants[i] = ref
			replaced = true
			break
		}
	}
	if !replaced {
		if len(root.VariantIndex.Variants) >= v.maxLimit {
			_ = v.evictOldest(c, root)
		}
		if len(root.VariantIndex.Variants) >= v.maxLimit {
			_ = c.bucket.Discard(context.Background(), object.NewVirtualID(baseID.Path(), ref.ObjectKey))
			return
		}
		root.VariantIndex.Variants = append(root.VariantIndex.Variants, ref)
	}
	root.VariantIndex.Revision++
	root.LastRefUnix = time.Now().Unix()
	_ = c.bucket.Store(context.Background(), root)
}

func (v *VaryProcessor) evictOldest(c *Caching, root *object.Metadata) bool {
	oldest := -1
	var oldestAt int64
	for i, ref := range root.VariantIndex.Variants {
		at := ref.StoredAtUnix
		if md, _ := c.bucket.Lookup(context.Background(), object.NewVirtualID(root.ID.Path(), ref.ObjectKey)); md == nil {
			oldest = i
			break
		} else if md.LastRefUnix > 0 {
			at = md.LastRefUnix
		}
		if oldest < 0 || at < oldestAt {
			oldest, oldestAt = i, at
		}
	}
	if oldest >= 0 {
		ref := root.VariantIndex.Variants[oldest]
		err := c.bucket.Discard(context.Background(), object.NewVirtualID(root.ID.Path(), ref.ObjectKey))
		if err != nil && !os.IsNotExist(err) {
			return false
		}
		root.VariantIndex.Variants = slices.Delete(root.VariantIndex.Variants, oldest, oldest+1)
		return true
	}
	return false
}

func variantObjectKey(schemaID, selector string, dimensions []object.VariantDimension) string {
	hash := sha256.New()
	for _, dim := range dimensions {
		_, _ = hash.Write([]byte(dim.Namespace))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(dim.Value))
		_, _ = hash.Write([]byte{0})
	}
	dimensionDigest := base64.RawURLEncoding.EncodeToString(hash.Sum(nil)[:9])
	return "vk2." + schemaID + "." + selector + "." + dimensionDigest
}

func collapsedFlightKey(base string, req *http.Request) string {
	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		lower := strings.ToLower(name)
		switch lower {
		case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
			"te", "trailer", "transfer-encoding", "upgrade", "traceparent", "tracestate",
			"x-request-id":
			continue
		}
		if strings.HasPrefix(lower, "x-protocol-") {
			continue
		}
		names = append(names, lower)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		for _, value := range req.Header.Values(name) {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return base + "." + base64.RawURLEncoding.EncodeToString(hash.Sum(nil)[:12])
}

func NewVaryProcessor(opts ...VaryOption) *VaryProcessor {
	v := &VaryProcessor{maxLimit: 100, varyIgnoreKey: make(map[string]struct{})}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

func WithVaryMaxLimit(limit int) VaryOption {
	return func(v *VaryProcessor) { v.maxLimit = limit }
}

func WithVaryIgnoreKeys(keys ...string) VaryOption {
	return func(v *VaryProcessor) {
		for _, key := range keys {
			if key = strings.ToLower(strings.TrimSpace(key)); key != "" && key != "*" {
				v.varyIgnoreKey[key] = struct{}{}
			}
		}
	}
}
