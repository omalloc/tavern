package ioindexes

func Build(start, end, partSize uint64) []uint32 {
	firstIndex := start / partSize
	lastIndex := end/partSize + 1

	parts := make([]uint32, 0, lastIndex-firstIndex)
	for i := firstIndex; i < lastIndex; i++ {
		parts = append(parts, uint32(i))
	}

	return parts
}
