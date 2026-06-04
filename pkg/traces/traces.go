package traces

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
)

type traceKey struct{}

type Trace struct {
	StartAt           time.Time
	RequestID         string
	RecvReq           uint64
	SentResp          uint64
	StoreUrl          string
	CacheStatus       string
	RemoteAddr        string
	FirstResponseTime time.Time
}

func (t *Trace) Clone() *Trace {
	out := *t
	return &out
}

func WithTrace(req *http.Request) (*http.Request, *Trace) {
	t := &Trace{
		StartAt:   time.Now(),
		RequestID: MustParseRequestID(req.Header),
	}
	return req.WithContext(NewContext(req.Context(), t)), t
}

func FromContext(ctx context.Context) *Trace {
	if v, ok := ctx.Value(traceKey{}).(*Trace); ok {
		return v
	}
	return &Trace{}
}

func NewContext(ctx context.Context, t *Trace) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

func MustParseRequestID(h http.Header) string {
	id := h.Get(protocol.ProtocolRequestIDKey)
	if id == "" {
		return generateRequestID()
	}
	return id
}

func RequestID() log.Valuer {
	return func(ctx context.Context) interface{} {
		if ctx == nil {
			return ""
		}

		if info := FromContext(ctx); info != nil {
			return info.RequestID
		}
		return ""
	}
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
