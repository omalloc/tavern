package object

import (
	"testing"

	"github.com/omalloc/tavern/pkg/encoding"
	"github.com/omalloc/tavern/pkg/encoding/cobr"
	"github.com/omalloc/tavern/pkg/encoding/json"
	"github.com/stretchr/testify/assert"
)

func TestIDMarshal(t *testing.T) {
	id := NewID("path/to/object")

	var codec encoding.Codec

	codec = &json.JSONCodec{}

	data, err := codec.Marshal(id)

	assert.NoError(t, err, "should marshal without error")

	var id1 ID
	err = codec.Unmarshal(data, &id1)
	assert.NoError(t, err, "should unmarshal without error")

	assert.NotEqual(t, id.String(), "", "should unmarshal to valid ID")

	codec = &cobr.CborCodec{}

	data, err = codec.Marshal(id)

	assert.NoError(t, err)

	var id2 ID
	err = codec.Unmarshal(data, &id2)

	assert.NoError(t, err, "should unmarshal without error")

	assert.NotEqual(t, id2.String(), "", "should unmarshal to valid ID")
}

func BenchmarkIDMarshalJSON(b *testing.B) {
	codec := &json.JSONCodec{}

	id := NewID("path/to/object")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := codec.Marshal(id)
		if err != nil {
			b.Fatalf("marshal failed: %v", err)
		}
	}
}

func BenchmarkIDMarshalCBor(b *testing.B) {
	codec := &cobr.CborCodec{}

	id := NewID("path/to/object")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := codec.Marshal(id)
		if err != nil {
			b.Fatalf("marshal failed: %v", err)
		}
	}
}

func BenchmarkIDUnmarshalJSON(b *testing.B) {
	codec := &json.JSONCodec{}

	id := NewID("path/to/object")
	data, err := codec.Marshal(id)
	if err != nil {
		b.Fatalf("marshal failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var id ID
		if err := codec.Unmarshal(data, &id); err != nil {
			b.Fatalf("unmarshal failed: %v", err)
		}
	}
}

func BenchmarkIDUnmarshalCBor(b *testing.B) {
	codec := &cobr.CborCodec{}

	id := NewID("path/to/object")
	data, err := codec.Marshal(id)
	if err != nil {
		b.Fatalf("marshal failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var id ID
		if err := codec.Unmarshal(data, &id); err != nil {
			b.Fatalf("unmarshal failed: %v", err)
		}
	}
}
