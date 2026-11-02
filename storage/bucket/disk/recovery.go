package disk

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
)

var _ storage.UnindexedChunkRecoverer = (*diskBucket)(nil)

// The bucket has exclusive writer ownership and the manager has not started
// accepting requests. Walk only the fixed hash directories, using bounded
// directory pages, without entering index/control directories or following links.
func (d *diskBucket) RecoverUnindexedChunks(ctx context.Context) error {
	return readDirectoryPages(ctx, d.path, func(first os.DirEntry) error {
		if !first.IsDir() || !hexDirectory(first.Name(), 1) {
			return nil
		}
		parent := filepath.Join(d.path, first.Name())
		return readDirectoryPages(ctx, parent, func(second os.DirEntry) error {
			if !second.IsDir() || !hexDirectory(second.Name(), 2) {
				return nil
			}
			dir := filepath.Join(parent, second.Name())
			return readDirectoryPages(ctx, dir, func(entry os.DirEntry) error {
				if !entry.Type().IsRegular() {
					return nil
				}
				name, suffix, found := strings.Cut(entry.Name(), "-")
				if !found || !hexDirectory(name, object.IdHashSize*2) || !strings.HasPrefix(name, first.Name()) || name[2:4] != second.Name() {
					return nil
				}
				key, err := hex.DecodeString(name)
				if err != nil {
					return nil
				}
				indexText, temporary, isTemporary := strings.Cut(suffix, ".tmp")
				index, err := strconv.ParseUint(indexText, 10, 32)
				if err != nil || len(indexText) < 6 || !decimalDigits(indexText) || len(indexText) > 6 && indexText[0] == '0' ||
					isTemporary && (len(temporary) != 14 || !decimalDigits(temporary)) {
					return nil
				}
				md, err := d.indexdb.Get(ctx, key)
				if err != nil && !errors.Is(err, storage.ErrKeyNotFound) {
					return err
				}
				if !isTemporary && err == nil && md != nil && md.ID != nil && md.ID.HashStr() == name && md.IsResponse() &&
					(md.Chunks.Contains(uint32(index)) || md.Parts.Contains(uint32(index))) {
					return nil
				}
				if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				return nil
			})
		})
	})
}

func hexDirectory(name string, size int) bool {
	return len(name) == size && strings.Trim(name, "0123456789abcdef") == ""
}

func decimalDigits(name string) bool { return strings.Trim(name, "0123456789") == "" }

func readDirectoryPages(ctx context.Context, path string, visit func(os.DirEntry) error) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
