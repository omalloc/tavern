package encoding

import (
	"sync"

	"github.com/omalloc/tavern/pkg/encoding/json"
)

var (
	mu           sync.Mutex
	defaultCodec Codec = json.JSONCodec{}
)

// Codec defines the interface gRPC uses to encode and decode messages.  Note
// that implementations of this interface must be thread safe; a Codec's
// methods can be called from concurrent goroutines.
type Codec interface {
	// Marshal returns the wire format of v.
	Marshal(v any) ([]byte, error)
	// Unmarshal parses the wire format into v.
	Unmarshal(data []byte, v any) error
	// Name returns the name of the Codec implementation. The returned string
	// will be used as part of content type in transmission.  The result must be
	// static; the result cannot change between calls.
	Name() string
}

// SetDefaultCodec sets the default codec.
func SetDefaultCodec(codec Codec) {
	mu.Lock()
	defer mu.Unlock()

	defaultCodec = codec
}

func GetDefaultCodec() Codec {
	mu.Lock()
	defer mu.Unlock()

	return defaultCodec
}

func Marshal(v any) ([]byte, error) {
	return GetDefaultCodec().Marshal(v)
}

func Unmarshal(data []byte, v any) error {
	return GetDefaultCodec().Unmarshal(data, v)
}

func Name() string {
	return GetDefaultCodec().Name()
}
