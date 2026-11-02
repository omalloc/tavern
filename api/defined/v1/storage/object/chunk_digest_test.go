package object

import (
	"testing"

	"github.com/omalloc/tavern/pkg/encoding"
)

func TestChunkDigestsOwnSnapshotsAndSurviveCodec(t *testing.T) {
	info := &RepresentationInfo{}
	info.RecordChunkDigest(3, []byte("complete chunk"))
	clone := info.Clone()
	clone.RecordChunkDigest(3, []byte("different bytes"))
	if clone.ChunkDigests[3] == info.ChunkDigests[3] {
		t.Fatal("metadata snapshots shared the digest map")
	}
	codec := encoding.GetDefaultCodec()
	data, err := codec.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RepresentationInfo
	if err := codec.Unmarshal(data, &decoded); err != nil || decoded.ChunkDigests[3] != info.ChunkDigests[3] {
		t.Fatal("persisted digest changed", decoded.ChunkDigests, err)
	}
}
