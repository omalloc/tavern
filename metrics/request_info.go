package metrics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/protocol"
)

type requestMetricKey struct{}

type RequestMetric struct {
	StartAt           time.Time
	RequestID         string
	RecvReq           uint64
	SentResp          uint64
	StoreUrl          string
	CacheStatus       string
	RemoteAddr        string
	FirstResponseTime time.Time
}

func (r *RequestMetric) Clone() *RequestMetric {
	out := *r
	return &out
}

func WithRequestMetric(req *http.Request) (*http.Request, *RequestMetric) {
	metric := &RequestMetric{
		StartAt:   time.Now(),
		RequestID: MustParseRequestID(req.Header), // for example, generate a unique request ID. you can use ParseeaderRequestID to get it later.
	}
	return req.WithContext(newContext(req.Context(), metric)), metric
}

func FromContext(ctx context.Context) *RequestMetric {
	if v, ok := ctx.Value(requestMetricKey{}).(*RequestMetric); ok {
		return v
	}
	return &RequestMetric{}
}

func NewContext(ctx context.Context, metric *RequestMetric) context.Context {
	return newContext(ctx, metric)
}

func newContext(ctx context.Context, metric *RequestMetric) context.Context {
	return context.WithValue(ctx, requestMetricKey{}, metric)
}

func MustParseRequestID(h http.Header) string {
	id := h.Get(protocol.ProtocolRequestIDKey)
	// protocol request id header not found, generate a new one
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
