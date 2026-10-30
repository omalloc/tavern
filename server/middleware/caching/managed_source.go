package caching

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/contrib/log"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
	"github.com/omalloc/tavern/storage/representation"
	planning "github.com/omalloc/tavern/transform"
)

func (p *managedProvider) sourceRequest(req *http.Request, plan planning.Plan) (*http.Request, error) {
	target, err := url.Parse(plan.SourceURL)
	if err != nil {
		return nil, planning.ErrInvalidParameter
	}
	original := prepareRepresentationRequest(req)
	host := original.Host
	if host == "" {
		host = original.URL.Host
	}
	if !strings.EqualFold(target.Host, host) || !strings.EqualFold(target.Scheme, original.URL.Scheme) || target.User != nil {
		return nil, planning.ErrInvalidParameter
	}
	result := req.Clone(planning.WithoutPlan(req.Context()))
	result.URL = target
	result.Host = target.Host
	result.RequestURI = ""
	result.Method = http.MethodGet
	result.Body = http.NoBody
	result.GetBody = nil
	result.ContentLength = 0
	result.TransferEncoding = nil
	for name := range result.Header {
		lower := strings.ToLower(name)
		if lower == "range" || lower == "if-none-match" || lower == "if-modified-since" || lower == "if-match" || lower == "if-unmodified-since" || lower == "if-range" || lower == "content-length" || strings.HasPrefix(lower, "x-tavern-") {
			delete(result.Header, name)
		}
	}
	inputs := append([]string(nil), p.caps.Inputs...)
	sort.Strings(inputs)
	result.Header.Set("Accept", strings.Join(inputs, ", "))
	result.Header.Set("Accept-Encoding", "identity")
	return result, nil
}
func (p *managedProvider) source(req *http.Request, plan planning.Plan) (fetched *managedSource, failure error) {
	delegated := false
	defer func() {
		if failure != nil && !delegated {
			pkgmetrics.RecordSourceFetch("failed")
			pkgmetrics.RecordVaryLookup("bypass", "control_error")
		}
	}()
	request, err := p.sourceRequest(req, plan)
	if err != nil {
		return nil, err
	}
	c := &Caching{req: request, opt: &p.opt}
	key, err := c.representationKey(request)
	if err != nil {
		return nil, err
	}
	repo, err := p.manager.Repository(key)
	if err != nil {
		return nil, err
	}
	source := &managedSource{repo: repo, key: key, request: request, preserveOnly: plan.Options.Format == "source"}
	encoded, _ := json.Marshal(request.Header)
	digest := sha256.Sum256(encoded)
	unlock, err := p.sources.lock(req.Context(), fmt.Sprintf("%s/%x", key.ResourceID(), digest))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if !requestBypassesCache(request) {
		selected, err := repo.LookupOriginMetadata(req.Context(), key, representationSelectionHeader(request))
		if err == nil && selected.Fresh && selected.Representation.HasComplete() {
			source.selected = selected
			source.headers = selected.Representation.Headers.Clone()
			if source.preserveOnly || preserveManagedSource(request, source.headers) {
				source.info, err = originalSourceInfo(source.headers, int64(selected.Representation.Size))
				if err != nil {
					return nil, err
				}
				pkgmetrics.RecordSourceFetch("hit")
				pkgmetrics.RecordVaryLookup("hit", "matched")
				return source, nil
			}
			if info, ok := p.imageInfo(selected.Representation); ok {
				source.info = info
				pkgmetrics.RecordSourceFetch("hit")
				pkgmetrics.RecordVaryLookup("hit", "matched")
				return source, nil
			}
			blob, err := p.decodeCachedSource(req.Context(), source)
			if err != nil {
				return nil, err
			}
			source.blob = blob
			source.info = blob.Info()
			if err = p.annotateSource(req.Context(), source); err != nil {
				source.close()
				return nil, err
			}
			pkgmetrics.RecordSourceFetch("hit")
			pkgmetrics.RecordVaryLookup("hit", "matched")
			return source, nil
		}
		if err != nil && !errors.Is(err, storage.ErrKeyNotFound) {
			return nil, err
		}
	}
	delegated = true
	return p.fetchSource(source)
}
func (p *managedProvider) imageInfo(md *object.Metadata) (transformv1.SourceInfo, bool) {
	info := md.Representation.Image
	if info == nil || info.SchemaVersion != 1 || info.InspectorVersion != p.inspector.Version() || info.DecodedSize <= 0 || len(info.DecodedDigest) != 64 || info.Width <= 0 || info.Height <= 0 {
		return transformv1.SourceInfo{}, false
	}
	if _, err := hex.DecodeString(info.DecodedDigest); err != nil {
		return transformv1.SourceInfo{}, false
	}
	return transformv1.SourceInfo{MIME: info.MIME, Size: info.DecodedSize, Width: info.Width, Height: info.Height, HasAlpha: info.HasAlpha, Animated: info.Animated, HasMetadata: info.HasMetadata}, true
}
func (p *managedProvider) fetchSource(source *managedSource) (fetched *managedSource, failure error) {
	outcome := "miss"
	defer func() {
		if failure != nil {
			outcome = "failed"
		}
		pkgmetrics.RecordSourceFetch(outcome)
	}()
	request := source.request
	c := &Caching{log: log.Context(request.Context()), ctx: request.Context(), req: request, opt: &p.opt, origin: p.origin, taskStore: p.store, representationRepo: source.repo}
	defer func() { c.representationLease.Close() }()
	// Lookup owns any old location used by a conditional 304 across migration.
	if err := c.lookupRepresentation(p.store); err != nil {
		return nil, err
	}
	response, err := c.proxyRepresentation(request)
	if err != nil {
		closeBody(response)
		return nil, err
	}
	defer closeBody(response)
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("image source returned status %d", response.StatusCode)
	}
	if c.cacheStatus == storage.CacheRevalidateHit {
		outcome = "revalidate"
		selected, err := source.repo.LookupOriginMetadata(request.Context(), source.key, representationSelectionHeader(request))
		if err == nil && selected.Fresh {
			if info, ok := p.imageInfo(selected.Representation); ok {
				source.selected, source.headers, source.info = selected, selected.Representation.Headers.Clone(), info
				return source, nil
			}
		}
	}

	raw, err := newManagedRaw(p.limits.Directory)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			raw.Close()
		}
	}()

	var blob *planning.FileBlob
	if source.preserveOnly || preserveManagedSource(request, response.Header) {
		var n int64
		n, err = io.Copy(raw.writer(), io.LimitReader(&managedWireReader{request.Context(), response.Body}, p.limits.InputBytes+1))
		if err == nil && n > p.limits.InputBytes {
			err = planning.ErrBudget
		}
		if err == nil && n == 0 {
			err = planning.ErrUnsupportedSource
		}
	} else {
		blob, err = planning.SpoolDecoded(request.Context(), io.TeeReader(response.Body, raw.writer()), strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))), p.limits.Directory, p.limits.InputBytes, p.limits.Pixels, p.inspector)
	}
	if err != nil {
		if c.id != nil && c.id.IsRepresentation() {
			if rejected, lookupErr := source.repo.LookupOriginMetadata(request.Context(), source.key, representationSelectionHeader(request)); lookupErr == nil && rejected.Representation.ID.Hash() == c.id.Hash() {
				source.repo.Retire(request.Context(), source.key, rejected.Reference)
			}
		}
		return nil, err
	}

	if err = raw.complete(); err != nil {
		if blob != nil {
			blob.Close()
		}
		return nil, err
	}
	if response.ContentLength >= 0 && response.ContentLength != raw.size {
		if blob != nil {
			blob.Close()
		}
		return nil, io.ErrUnexpectedEOF
	}
	source.raw = raw
	retained = true
	source.blob, source.headers = blob, response.Header.Clone()
	if blob != nil {
		source.info = blob.Info()
	} else {
		source.info, err = originalSourceInfo(source.headers, raw.size)
	}
	success := false
	defer func() {
		if !success {
			source.close()
		}
	}()

	if err != nil {
		return nil, err
	}
	declared, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || declared != source.info.MIME {
		return nil, planning.ErrUnsupportedSource
	}
	if !requestBypassesCache(request) && c.id != nil && c.id.IsRepresentation() {
		selected, err := source.repo.LookupOriginMetadata(request.Context(), source.key, representationSelectionHeader(request))
		if err == nil && selected.Fresh && selected.Representation.HasComplete() && selected.Representation.ID.Hash() == c.id.Hash() {
			source.selected = selected
			if blob != nil {
				if err = p.annotateSource(request.Context(), source); err != nil {
					return nil, err
				}
			}
		} else if err != nil && !errors.Is(err, storage.ErrKeyNotFound) {
			return nil, err
		}
	}
	success = true
	return source, nil
}
func (p *managedProvider) annotateSource(ctx context.Context, source *managedSource) error {
	state, err := source.repo.Capture(ctx, source.key)
	if err != nil {
		return err
	}
	previous := source.selected.Reference
	lease, err := source.repo.ReserveFor(ctx, state, previous, p.opt.VaryLimit)
	if err != nil {
		return err
	}
	md := source.selected.Representation.Clone()
	info := source.info
	md.Representation.Image = &object.ImageInfo{SchemaVersion: 1, InspectorVersion: p.inspector.Version(), DecodedDigest: source.blob.Digest(), MIME: info.MIME, DecodedSize: info.Size, Width: info.Width, Height: info.Height, HasAlpha: info.HasAlpha, Animated: info.Animated, HasMetadata: info.HasMetadata}
	bucket := p.bucket(state.Resource().Resource.ActiveLocation)
	if bucket == nil {
		source.repo.Release(lease)
		return storage.ErrKeyNotFound
	}
	published, err := source.repo.Publish(ctx, state, lease, previous, md, &previous, func() error { return bucket.Store(ctx, md) })
	if err != nil {
		return err
	}
	if !published {
		return representation.ErrSuperseded
	}
	selected, err := source.repo.LookupOriginMetadata(ctx, source.key, representationSelectionHeader(source.request))
	if err != nil {
		return err
	}
	if selected.Representation.ID.Hash() != md.ID.Hash() {
		return representation.ErrSuperseded
	}
	source.selected = selected
	source.headers = selected.Representation.Headers.Clone()
	return nil
}
func (p *managedProvider) bucket(location string) storage.Bucket {
	buckets := p.store.Buckets()
	if provider, ok := p.store.(storage.RepresentationBucketProvider); ok {
		buckets = provider.RepresentationBuckets()
	}
	for _, bucket := range buckets {
		if bucket.ID() == location {
			return bucket
		}
	}
	return nil
}
func (p *managedProvider) decodeCachedSource(ctx context.Context, source *managedSource) (*planning.FileBlob, error) {
	selected, err := source.repo.LookupRead(ctx, source.key, representationSelectionHeader(source.request))
	if err != nil {
		return nil, err
	}
	defer selected.Lease.Close()
	if source.selected.Representation.ID.Hash() != selected.Representation.ID.Hash() {
		return nil, representation.ErrSuperseded
	}
	reader, err := p.openLookup(ctx, selected)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	blob, err := planning.SpoolDecoded(ctx, reader, strings.ToLower(strings.TrimSpace(selected.Representation.Headers.Get("Content-Encoding"))), p.limits.Directory, p.limits.InputBytes, p.limits.Pixels, p.inspector)
	if err != nil {
		source.repo.Retire(ctx, source.key, selected.Reference)
		return nil, err
	}
	declared, _, parseErr := mime.ParseMediaType(selected.Representation.Headers.Get("Content-Type"))
	if parseErr != nil || declared != blob.Info().MIME {
		blob.Close()
		source.repo.Retire(ctx, source.key, selected.Reference)
		return nil, planning.ErrUnsupportedSource
	}
	if _, known := p.imageInfo(selected.Representation); known && selected.Representation.Representation.Image.DecodedDigest != blob.Digest() {
		blob.Close()
		source.repo.Retire(ctx, source.key, selected.Reference)
		return nil, planning.ErrUnsupportedSource
	}
	return blob, nil
}
func (p *managedProvider) openLookup(ctx context.Context, selected *representation.LookupResult) (io.ReadCloser, error) {
	bucket := p.bucket(selected.Lease.Location())
	if bucket == nil {
		return nil, storage.ErrKeyNotFound
	}
	md := selected.Representation
	return &managedChunkReader{ctx: ctx, bucket: bucket, id: md.ID.Clone(), size: md.Size, blockSize: md.BlockSize}, nil
}

// Open one local file at a time; small slices cannot exhaust descriptors merely
// because a complete source/product contains many chunks.
type managedChunkReader struct {
	ctx                       context.Context
	bucket                    storage.Bucket
	id                        *object.ID
	size, blockSize, position uint64
	file                      storage.File
	mu                        sync.Mutex
	closed                    bool
}

func (r *managedChunkReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.position == r.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	index := uint32(r.position / r.blockSize)
	offset := r.position % r.blockSize
	if r.file == nil {
		file, _, err := r.bucket.ReadChunkFile(r.ctx, r.id, index)
		if err != nil {
			return 0, err
		}
		stat, err := file.Stat()
		if err != nil || stat.Size() != int64(min(r.blockSize, r.size-uint64(index)*r.blockSize)) {
			file.Close()
			if err != nil {
				return 0, err
			}
			return 0, io.ErrUnexpectedEOF
		}
		r.file = file
	}
	take := min(uint64(len(p)), r.blockSize-offset, r.size-r.position)
	n, err := r.file.ReadAt(p[:take], int64(offset))
	r.position += uint64(n)
	if err == io.EOF && uint64(n) != take {
		return n, io.ErrUnexpectedEOF
	}
	if r.position%r.blockSize == 0 || r.position == r.size {
		closeErr := r.file.Close()
		r.file = nil
		if err == nil {
			err = closeErr
		}
	}
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}
func (r *managedChunkReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}

func (r *managedChunkReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	next, err := managedSeekPosition(int64(r.position), int64(r.size), offset, whence)
	if err != nil {
		return 0, err
	}
	if r.file != nil {
		if err = r.file.Close(); err != nil {
			return 0, err
		}
		r.file = nil
	}
	r.position = uint64(next)
	return next, nil
}

// Original wire bytes are retained separately from decoded conversion input.
// This preserves content coding and validators for private/no-transform fallback.
type managedRaw struct {
	file *os.File
	path string
	hash hash.Hash
	size int64
	once sync.Once
	err  error
}

func newManagedRaw(directory string) (*managedRaw, error) {
	file, err := os.CreateTemp(directory, "tavern-source-wire-*")
	if err != nil {
		return nil, err
	}
	return &managedRaw{file: file, path: file.Name(), hash: sha256.New()}, nil
}
func (r *managedRaw) writer() io.Writer { return io.MultiWriter(r.file, r.hash) }
func (r *managedRaw) complete() error {
	stat, err := r.file.Stat()
	if err != nil {
		return err
	}
	r.size = stat.Size()
	return r.file.Close()
}
func (r *managedRaw) open(ctx context.Context) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.Open(r.path)
}
func (r *managedRaw) Close() error {
	r.once.Do(func() {
		r.file.Close()
		r.err = os.Remove(r.path)
		if errors.Is(r.err, os.ErrNotExist) {
			r.err = nil
		}
	})
	return r.err
}

func originalSourceInfo(headers http.Header, size int64) (transformv1.SourceInfo, error) {
	mimeType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil || strings.Contains(mimeType, "*") || size <= 0 {
		return transformv1.SourceInfo{}, planning.ErrUnsupportedSource
	}
	return transformv1.SourceInfo{MIME: mimeType, Size: size}, nil
}

type managedWireReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *managedWireReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
