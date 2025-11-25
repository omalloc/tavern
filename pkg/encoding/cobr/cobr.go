package cobr

import "github.com/fxamacker/cbor/v2"

type CborCodec struct{}

// Marshal implements Codec.
func (c *CborCodec) Marshal(v any) ([]byte, error) {
	return cbor.Marshal(v)
}

// Unmarshal implements Codec.
func (c *CborCodec) Unmarshal(data []byte, v any) error {
	return cbor.Unmarshal(data, v)
}

// Name implements Codec.
func (c *CborCodec) Name() string {
	return "cbor"
}
