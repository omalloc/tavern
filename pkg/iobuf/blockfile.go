package iobuf

import (
	"slices"

	"github.com/kelindar/bitmap"
)

const BitBlock = 1 << 15

func FullHit(first, last uint32, fs bitmap.Bitmap) bool {
	for i := first; i <= last; i++ {
		if !fs.Contains(i) {
			return false
		}
	}
	return true
}

func PartHit(first, last uint32, fs bitmap.Bitmap) bool {
	for i := first; i <= last; i++ {
		if fs.Contains(i) {
			return true
		}
	}
	return false
}

func BreakInBitmap(start, end int64, partSize int64) bitmap.Bitmap {
	bm := bitmap.Bitmap{}
	firstIndex := start / partSize
	lastIndex := end/partSize + 1

	for i := firstIndex; i < lastIndex; i++ {
		bm.Set(uint32(i))
	}
	return bm
}

type Block struct {
	Match      bool     // true: hit, false: miss
	BlockRange []uint32 // [first, ..., last]
}

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

func BufBlock(blocks []uint32) (offset, limit int64) {
	offset = int64(blocks[0] * BitBlock)
	limit = int64((blocks[len(blocks)-1])*BitBlock) + BitBlock
	return
}

func ChunkPart(blocks []uint32, partSize uint32) (offset, limit int64) {
	offset = int64(blocks[0] * partSize)
	limit = int64((blocks[len(blocks)-1])*partSize) + int64(partSize)
	return
}

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
