package verifier

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
)

func buildPaths(basepath string, cacheKey string, count int) []string {
	paths := make([]string, 0, count)
	for i := range count {
		paths = append(paths, filepath.Join(basepath, fmt.Sprintf("%s-%06d", cacheKey, i)))
	}
	return paths
}

func ReadAndSumHash(basepath string, cacheKey string, count int, chunkSzie uint64) (string, error) {

	paths := buildPaths(basepath, cacheKey, count)

	h := md5.New()

	readChunkFile := func(fileName string, idx int, h hash.Hash) error {
		f, err := os.OpenFile(fileName, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()

		n, err := f.WriteTo(h)
		if err != nil {
			return err
		}

		if idx > count && uint64(n) != chunkSzie {
			return fmt.Errorf("file %s size %d not equal chunk size %d", fileName, n, chunkSzie)
		}

		return nil
	}

	for i, path := range paths {
		if err := readChunkFile(path, i, h); err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
