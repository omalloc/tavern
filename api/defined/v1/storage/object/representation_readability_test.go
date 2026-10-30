package object

import (
	"math"
	"testing"
)

func TestRepresentationPartialReadability(t *testing.T) {
	_, md := representationFixture(t)
	md.Size = 150
	md.Chunks.Clear()
	md.Chunks.Set(1)
	md.Representation.State = RepresentationFilling
	if md.HasComplete() || !md.HasReadableChunks() || !md.HasReadableRange(70, 127) || md.HasReadableRange(63, 127) || md.HasReadableRange(64, 128) {
		t.Fatal("partial generation exposed missing bytes")
	}
	for _, mutate := range []func(*Metadata){
		func(m *Metadata) { m.Parts.Set(2) },
		func(m *Metadata) { m.Chunks.Set(3) },
		func(m *Metadata) { m.Representation.State = RepresentationFailed },
		func(m *Metadata) { m.Representation.State = RepresentationComplete },
		func(m *Metadata) { m.Chunks.Clear() },
		func(m *Metadata) { m.Size = 0 },
		func(m *Metadata) { m.BlockSize = 0 },
		func(m *Metadata) { m.Size = math.MaxUint64 },
		func(m *Metadata) { m.BlockSize = 1; m.Size = uint64(math.MaxUint32) + 2 },
	} {
		copy := md.Clone()
		mutate(copy)
		if copy.HasReadableChunks() || copy.HasReadableRange(70, 79) {
			t.Fatal("corrupt/unfinished generation was readable", copy)
		}
	}
	md.Chunks.Set(0)
	md.Chunks.Set(2)
	if md.HasReadableChunks() {
		t.Fatal("full bitmap without the EOF state transition was readable")
	}
	md.Representation.State = RepresentationComplete
	if !md.HasReadableRange(0, 149) || md.HasReadableRange(0, 150) || md.HasReadableRange(2, 1) {
		t.Fatal("complete range bounds were not enforced")
	}
}
