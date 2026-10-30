package caching

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
	"github.com/omalloc/tavern/storage/representation"
	planning "github.com/omalloc/tavern/transform"
)

func (p *managedProvider) sourceResult(ctx context.Context, source *managedSource) (*managedResult, error) {
	if source.selected != nil {
		selected, err := source.repo.LookupRead(ctx, source.key, representationSelectionHeader(source.request))
		if err != nil {
			return nil, err
		}
		if selected.Representation.ID.Hash() != source.selected.Representation.ID.Hash() {
			selected.Lease.Close()
			return nil, representation.ErrSuperseded
		}
		return p.cachedResult(source, selected, true)
	}
	blob := source.blob
	source.blob = nil
	md := &object.Metadata{Code: 200, Headers: outputHeaders(source.headers, blob.Info(), blob.Digest(), true), Size: uint64(blob.Info().Size)}
	return &managedResult{Metadata: md, Info: blob.Info(), SourceHeaders: source.headers.Clone(), open: blob.Open, finish: blob.Close}, nil
}
func (p *managedProvider) cachedResult(source *managedSource, selected *representation.LookupResult, hit bool) (*managedResult, error) {
	md := selected.Representation.Clone()
	info := source.info
	if image := md.Representation.Image; image != nil {
		info = transformv1.SourceInfo{MIME: image.MIME, Size: image.DecodedSize, Width: image.Width, Height: image.Height, HasAlpha: image.HasAlpha, Animated: image.Animated}
	}
	open := func(ctx context.Context) (io.ReadCloser, error) {
		lease := selected.Lease.Duplicate()
		if lease == nil {
			return nil, io.ErrClosedPipe
		}
		pinned := *selected
		pinned.Lease = lease
		reader, err := p.openLookup(ctx, &pinned)
		if err != nil {
			lease.Close()
			return nil, err
		}
		return &managedPinnedReader{ReadCloser: reader, lease: lease}, nil
	}
	return &managedResult{Metadata: md, Info: info, Cached: hit, SourceHeaders: source.headers.Clone(), open: open, finish: selected.Lease.Close}, nil
}

type managedPinnedReader struct {
	io.ReadCloser
	lease *representation.ReadLease
	once  sync.Once
	err   error
}

func (r *managedPinnedReader) Close() error {
	r.once.Do(func() { r.err = r.ReadCloser.Close(); r.lease.Close() })
	return r.err
}

func (p *managedProvider) convert(ctx context.Context, source *managedSource, spec planning.NormalizedSpec, state *representation.IndexState, lease *representation.Reservation) (result *managedResult, failure error) {
	var ref object.VariantRef
	var md *object.Metadata
	var bucket storage.Bucket
	published, publicationAttempted := false, false
	if source.selected != nil {
		var err error
		ref, err = representation.NewManagedSelector(source.selected, spec.Fingerprint())
		if err != nil {
			return nil, err
		}
		root := state.Resource().Resource
		generation := uuid.NewString()
		id, err := source.key.RepresentationID(root.Incarnation, root.PurgeEpoch, ref.SelectorFingerprint, generation)
		if err != nil {
			return nil, err
		}
		ref.RepresentationID, ref.ResponseDate = id, source.selected.Reference.ResponseDate
		md = &object.Metadata{ID: id, RecordKind: object.RecordRepresentation, Code: 200, Headers: make(http.Header), BlockSize: p.opt.SliceSize, RespUnix: source.selected.Representation.RespUnix, ExpiresAt: source.selected.Representation.ExpiresAt,
			Representation: &object.RepresentationInfo{SchemaVersion: object.RepresentationSchemaVersion, ResourceID: source.key.ResourceID(), Incarnation: root.Incarnation, PurgeEpoch: root.PurgeEpoch, ValidatedEpoch: root.RevalidateEpoch,
				State: object.RepresentationFilling, Kind: object.RepresentationDerived, BodyGeneration: generation, StorageSelectorFingerprint: ref.SelectorFingerprint, SourceID: ref.SelectedSourceID.Clone(), SourceVersion: ref.SelectedSourceVersion, TransformSpec: spec.JSON(), PolicyVersion: spec.PolicyVersion(), EncoderVersion: p.caps.EncoderVersion, SourceBoundTTL: true}}
		bucket = p.bucket(root.ActiveLocation)
		if bucket == nil {
			return nil, storage.ErrKeyNotFound
		}
		if err = source.repo.BindFill(ctx, state, lease, md); err != nil {
			return nil, err
		}
		defer func() {
			if !published && !publicationAttempted {
				bucket.Discard(context.WithoutCancel(ctx), md.ID)
			}
		}()
	}
	input := source.blob
	if input == nil {
		var err error
		input, err = p.decodeCachedSource(ctx, source)
		if err != nil {
			return nil, err
		}
		defer input.Close()
	}
	if input.Info() != source.info {
		if source.selected != nil {
			source.repo.Retire(ctx, source.key, source.selected.Reference)
		}
		return nil, planning.ErrUnsupportedSource
	}
	output, err := p.backend.Transform(ctx, input, spec.Spec())
	if output != nil {
		defer output.Close()
	}
	if err != nil {
		return nil, err
	}
	if output == nil {
		return nil, planning.ErrUnsupportedSource
	}
	info := output.Info()
	if info.MIME != spec.Spec().OutputMIME || info.Size <= 0 || info.Size > p.limits.OutputBytes || output.EncoderVersion() != p.caps.EncoderVersion {
		return nil, planning.ErrUnsupportedSource
	}
	if len(output.Digest()) != 64 {
		return nil, planning.ErrUnsupportedSource
	}
	if _, err = hex.DecodeString(output.Digest()); err != nil {
		return nil, planning.ErrUnsupportedSource
	}
	reader, err := output.Open(ctx)
	if err != nil {
		return nil, err
	}
	verified, err := planning.SpoolDecoded(ctx, reader, "identity", p.limits.Directory, p.limits.OutputBytes, p.limits.Pixels, p.inspector)
	reader.Close()
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			verified.Close()
		}
	}()
	actual := verified.Info()
	if actual.MIME != info.MIME || actual.Size != info.Size || actual.Width != source.info.Width || actual.Height != source.info.Height || actual.Animated || actual.MIME == "image/jpeg" && actual.HasAlpha || verified.Digest() != strings.ToLower(output.Digest()) {
		return nil, planning.ErrUnsupportedSource
	}
	headers := outputHeaders(source.headers, actual, verified.Digest(), source.selected == nil)
	if source.selected == nil {
		transferred = true
		return &managedResult{Metadata: &object.Metadata{Code: 200, Headers: headers, Size: uint64(actual.Size)}, Info: actual, SourceHeaders: source.headers.Clone(), open: verified.Open, finish: verified.Close}, nil
	}
	md.Size, md.Headers = uint64(actual.Size), headers
	md.Representation.Image = &object.ImageInfo{SchemaVersion: 1, InspectorVersion: p.inspector.Version(), DecodedDigest: verified.Digest(), MIME: actual.MIME, DecodedSize: actual.Size, Width: actual.Width, Height: actual.Height, HasAlpha: actual.HasAlpha, Animated: actual.Animated}
	if err = p.writeProduct(ctx, source, state, lease, bucket, md, verified); err != nil {
		return nil, err
	}
	md.Representation.State = object.RepresentationComplete
	publicationAttempted = true
	var previous *object.VariantRef
	for _, existing := range state.Resource().Resource.Variants {
		if existing.SelectionKind == object.SelectionManaged && existing.SelectorFingerprint == ref.SelectorFingerprint {
			copy := existing.Clone()
			previous = &copy
			break
		}
	}
	published, err = source.repo.Publish(ctx, state, lease, ref, md, previous, func() error { return bucket.Store(ctx, md) })
	if err != nil {
		return nil, err
	}
	if !published {
		publicationAttempted = false
		return nil, representation.ErrSuperseded
	}
	selected, err := source.repo.LookupDerivedRead(ctx, source.key, ref.SelectedSourceID, ref.SelectedSourceVersion, ref.TransformFingerprint)
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil, representation.ErrSuperseded
	}
	if err != nil {
		return nil, err
	}
	return p.cachedResult(source, selected, false)
}
func (p *managedProvider) writeProduct(ctx context.Context, source *managedSource, state *representation.IndexState, lease *representation.Reservation, bucket storage.Bucket, md *object.Metadata, blob *planning.FileBlob) error {
	reader, err := blob.Open(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	for offset := uint64(0); offset < md.Size; offset += md.BlockSize {
		if err = ctx.Err(); err != nil {
			return err
		}
		index := uint32(offset / md.BlockSize)
		md.Parts.Set(index)
		if err = source.repo.JournalFill(ctx, state, lease, bucket, md); err != nil {
			return err
		}
		writer, _, err := bucket.WriteChunkFile(ctx, md.ID, index)
		if err != nil {
			return err
		}
		n, writeErr := io.CopyN(writer, reader, int64(min(md.BlockSize, md.Size-offset)))
		closeErr := writer.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != int64(min(md.BlockSize, md.Size-offset)) {
			return io.ErrShortWrite
		}
		md.Parts.Remove(index)
		md.Chunks.Set(index)
		if err = bucket.Store(ctx, md); err != nil {
			return err
		}
	}
	var extra [1]byte
	if n, err := reader.Read(extra[:]); n != 0 || err != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func outputHeaders(source http.Header, info transformv1.SourceInfo, digest string, private bool) http.Header {
	headers := source.Clone()
	for _, name := range []string{"Content-Encoding", "Content-Range", "Content-Length", "Content-Type", "ETag", "Last-Modified", "Content-Md5", "Digest", "Content-Digest", "Repr-Digest", "Accept-Ranges"} {
		headers.Del(name)
	}
	headers.Set("Content-Type", info.MIME)
	headers.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	headers.Set("ETag", fmt.Sprintf(`"sha256-%s"`, digest))
	if private {
		headers.Set("Cache-Control", "no-store")
		headers.Del("Expires")
	}
	return headers
}
