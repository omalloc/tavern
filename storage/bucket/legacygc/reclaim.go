// Package legacygc reclaims only retired legacy Vary identities. It never follows
// VirtualKey strings into other objects. The storage owner must exclude legacy
// readers/writers and revoke root references across all buckets before Reclaim.
package legacygc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var ErrNotRetired = errors.New("metadata is not a reclaimable legacy Vary record")

func Eligible(md *object.Metadata) bool {
	return md != nil && md.ID != nil && md.IsLegacy() && !md.ID.IsControl() && !md.ID.IsRepresentation() && md.Flags & ^(object.FlagVaryIndex|object.FlagVaryCache|object.FlagChunkedCache) == 0 && md.IsLegacyVary()
}

type Reclaimer struct {
	Index   storage.IndexDB
	Journal func(context.Context, *object.Metadata) error
	Remove  func(context.Context, *object.ID, uint32, int) (int, bool, error)
}

func (r Reclaimer) RetireRoot(ctx context.Context, id *object.ID) error {
	if id == nil || id.IsControl() || id.IsRepresentation() {
		return storage.ErrProtectedControl
	}
	md, err := r.Index.Get(ctx, id.Bytes())
	if errors.Is(err, storage.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if md == nil || md.ID == nil || !bytes.Equal(md.ID.Bytes(), id.Bytes()) {
		return ErrNotRetired
	}
	if !Eligible(md) {
		return nil
	}
	copy := md.Clone()
	copy.VirtualKey = nil
	copy.Flags |= object.FlagVaryCache
	// Always confirm the journal, even if an earlier ambiguous sync already made
	// the empty reference list visible in memory.
	return r.Journal(ctx, copy)
}
func (r Reclaimer) Reclaim(ctx context.Context, id *object.ID, budget int) (processed int, done bool, failure error) {
	if budget < 2 {
		return 0, false, storage.ErrInvalidationBudget
	}
	if id == nil || id.IsControl() || id.IsRepresentation() {
		return 0, false, storage.ErrProtectedControl
	}
	md, err := r.Index.Get(ctx, id.Bytes())
	if errors.Is(err, storage.ErrKeyNotFound) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !Eligible(md) || !bytes.Equal(md.ID.Bytes(), id.Bytes()) {
		return 0, false, ErrNotRetired
	}
	copy := md.Clone()
	copy.VirtualKey = nil
	copy.Flags |= object.FlagVaryCache
	if err = r.Journal(ctx, copy); err != nil {
		return 0, false, err
	}
	chunks := copy.Chunks.Clone(nil)
	copy.Parts.Range(func(index uint32) { chunks.Set(index) })
	for processed+2 <= budget {
		index, ok := chunks.Min()
		if !ok {
			break
		}
		if err = ctx.Err(); err != nil {
			failure = err
			break
		}
		count, complete, err := r.Remove(ctx, id, index, budget-processed)
		processed += count
		if err != nil {
			failure = err
			break
		}
		if !complete {
			break
		}
		copy.Chunks.Remove(index)
		copy.Parts.Remove(index)
		chunks.Remove(index)
	}
	// Persist progress before returning, including partial/error passes. A crash
	// can leave extra missing bits to retry, never untracked live references.
	journalErr := r.Journal(context.WithoutCancel(ctx), copy)
	if failure != nil || journalErr != nil {
		return processed, false, errors.Join(failure, journalErr)
	}
	if chunks.Count() != 0 {
		return processed, false, nil
	}
	if err = ctx.Err(); err != nil {
		return processed, false, err
	}
	if err = r.Index.Delete(ctx, id.Bytes()); err != nil {
		return processed, false, err
	}
	return processed, true, nil
}

// RemoveDiskChunk bounds deletions (final and temporary files). Directory names
// are read in small batches and checked against the deadline; no unbounded Glob
// allocation and no age-based deletion are used. The owner has revoked this ID.
func RemoveDiskChunk(ctx context.Context, path string, budget int) (int, bool, error) {
	if budget < 2 {
		return 0, false, storage.ErrInvalidationBudget
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 1, false, err
	}
	processed := 1
	directory, err := os.Open(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return processed, true, nil
	}
	if err != nil {
		return processed, false, err
	}
	defer directory.Close()
	prefix := filepath.Base(path) + ".tmp"
	for {
		if err = ctx.Err(); err != nil {
			return processed, false, err
		}
		names, readErr := directory.Readdirnames(128)
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			if processed == budget {
				return processed, false, nil
			}
			if err = os.Remove(filepath.Join(filepath.Dir(path), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return processed + 1, false, err
			}
			processed++
		}
		if readErr == io.EOF {
			return processed, true, nil
		}
		if readErr != nil {
			return processed, false, readErr
		}
	}
}
