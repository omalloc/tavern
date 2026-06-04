// Package ioindexes provides a helper to convert a byte range into a list of
// block indices for chunked file storage.
package ioindexes

// Build returns the block indices that span the byte range [start, end] given a
// partSize. For example, with partSize=1MB, bytes 0–1048575 produce [0]; bytes
// 1048576–2097151 produce [1]; bytes 500000–2097150 produce [0, 1].
//
// The result is a compact []uint32 suitable for iterating over chunk files on disk.
// For set operations (union, intersection, difference) prefer the bitmap-based
// functions in the parent iobuf package.
func Build(start, end, partSize uint64) []uint32 {
	firstIndex := start / partSize
	lastIndex := end/partSize + 1

	parts := make([]uint32, 0, lastIndex-firstIndex)
	for i := firstIndex; i < lastIndex; i++ {
		parts = append(parts, uint32(i))
	}

	return parts
}
