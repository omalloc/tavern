package iobuf

import (
	"slices"

	"github.com/kelindar/bitmap"
)

// BitBlock is the default block size (32 KB) used by [BreakInBitmap] and [BufBlock]
// when breaking a byte range into logical blocks for bitmap indexing.
const BitBlock = 1 << 15

// FullHit returns true when every block index in [first, last] (inclusive) is present
// in the bitmap — meaning the entire requested range is cached locally.
//
// Used by the caching middleware to decide whether a Range request can be served
// entirely from disk without an upstream fetch.
func FullHit(first, last uint32, fs bitmap.Bitmap) bool {
	for i := first; i <= last; i++ {
		if !fs.Contains(i) {
			return false
		}
	}
	return true
}

// PartHit returns true when at least one block index in [first, last] is present
// in the bitmap — meaning a partial (possibly fragmented) cache hit.
//
// Used alongside FullHit to decide whether the request needs a range-fill
// (some blocks cached, some must be fetched from origin).
func PartHit(first, last uint32, fs bitmap.Bitmap) bool {
	for i := first; i <= last; i++ {
		if fs.Contains(i) {
			return true
		}
	}
	return false
}

// BreakInBitmap converts a byte range [start, end] into a bitmap where each set bit
// represents one block-sized chunk that the range spans. partSize is the chunk size
// (typically 1 MB in tavern's cache storage).
//
// The resulting bitmap is used to record which chunks of a file have been written to
// disk — set bits mean "this chunk is stored on disk".
func BreakInBitmap(start, end int64, partSize int64) bitmap.Bitmap {
	bm := bitmap.Bitmap{}
	firstIndex := start / partSize
	lastIndex := end/partSize + 1

	for i := firstIndex; i < lastIndex; i++ {
		bm.Set(uint32(i))
	}
	return bm
}

// Block describes a contiguous range of chunk indices and whether they are cached
// (Match=true) or missing (Match=false). A sorted list of Blocks is produced by
// [BlockGroup] to drive range-fill logic.
type Block struct {
	Match      bool     // true: hit, false: miss
	BlockRange []uint32 // [first, ..., last]
}

// BlockGroup compares the desired chunk set (want) against the locally-available set
// (hitter), producing a sorted list of [Block] entries grouped by consecutive hit/miss
// runs. The result drives the caching middleware's logic for stitching together cached
// file chunks and upstream range requests.
//
// Example: if want = {0,1,2,3,4} and hitter = {1,2,4}, the result is:
//
//	[miss:0] [hit:1,2] [miss:3] [hit:4]
func BlockGroup(hitter bitmap.Bitmap, want bitmap.Bitmap) []*Block {
	q1 := want.Clone(nil)
	q1.And(hitter) // HIT block

	hitRange := make([]uint32, 0)
	q1.Range(func(i uint32) {
		hitRange = append(hitRange, i)
	})

	hitGroup := groupBy(hitRange)

	missRange := make([]uint32, 0)
	want.AndNot(hitter) // MISS block
	want.Range(func(i uint32) {
		missRange = append(missRange, i)
	})

	missGroup := groupBy(missRange)

	result := make([]*Block, 0, len(hitGroup)+len(missGroup))
	for _, v := range hitGroup {
		result = append(result, &Block{Match: true, BlockRange: v})
	}
	for _, v := range missGroup {
		result = append(result, &Block{Match: false, BlockRange: v})
	}

	slices.SortFunc(result, func(a, b *Block) int {
		return int(a.BlockRange[0] - b.BlockRange[0])
	})

	return result
}

// BufBlock converts a run of block indices (as returned by [BlockGroup]) to a byte
// offset and total byte length using the default [BitBlock] size.
func BufBlock(blocks []uint32) (offset, limit int64) {
	offset = int64(blocks[0] * BitBlock)
	limit = int64((blocks[len(blocks)-1])*BitBlock) + BitBlock
	return
}

// ChunkPart converts a run of block indices to a byte offset and total byte length
// using a caller-supplied partSize (typically the cache slice_size).
func ChunkPart(blocks []uint32, partSize uint32) (offset, limit int64) {
	offset = int64(blocks[0] * partSize)
	limit = int64((blocks[len(blocks)-1])*partSize) + int64(partSize)
	return
}

// groupBy coalesces a sorted slice of uint32 values into runs of consecutive integers.
// For example, [0,1,2,5,6,9] becomes [[0,1,2],[5,6],[9]].
func groupBy(v []uint32) [][]uint32 {
	if len(v) == 0 {
		return nil
	}

	var ret [][]uint32
	group := []uint32{v[0]}
	for i := 1; i < len(v); i++ {
		if v[i] == v[i-1]+1 {
			group = append(group, v[i])
		} else {
			ret = append(ret, group)
			group = []uint32{v[i]}
		}
	}
	ret = append(ret, group)

	return ret
}
