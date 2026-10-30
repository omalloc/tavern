package caching

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/internal/protocol"
	planning "github.com/omalloc/tavern/transform"
)

// The complete provider result is bound before any client byte handling. P3.4
// adds conditional/Range assembly; until then those controlled requests fail
// explicitly rather than forwarding target offsets or source validators.
func (p *managedProvider) roundTrip(req *http.Request, plan planning.Plan) (*http.Response, error) {
	if req.Header.Get("Range") != "" || hasClientConditions(req) {
		return nil, errors.New("managed conditional/range response binding is not implemented")
	}
	result, err := p.resolve(req, plan)
	if err != nil {
		return nil, err
	}
	status := storage.CacheMiss
	if result.Metadata.ID == nil {
		status = storage.BYPASS
	} else if result.Cached {
		status = storage.CacheHit
	}
	response := &http.Response{StatusCode: http.StatusOK, Header: result.Metadata.Headers.Clone(), ContentLength: int64(result.Metadata.Size), Request: req}
	response.Header.Set(protocol.ProtocolCacheStatusKey, status.String())
	if req.Method == http.MethodHead {
		result.Close()
		response.Body = http.NoBody
		return response, nil
	}
	reader, err := result.Open(req.Context())
	if err != nil {
		result.Close()
		return nil, err
	}
	response.Body = &managedResponseBody{ReadCloser: reader, result: result}
	return response, nil
}

type managedResponseBody struct {
	io.ReadCloser
	result *managedResult
	once   sync.Once
	err    error
}

func (b *managedResponseBody) Close() error {
	b.once.Do(func() { b.err = errors.Join(b.ReadCloser.Close(), b.result.Close()) })
	return b.err
}
