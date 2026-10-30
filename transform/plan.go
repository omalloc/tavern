package transform

import (
	"context"
	"net/http"
	"slices"
	"strings"

	transformv1 "github.com/omalloc/tavern/api/defined/v1/transform"
)

// Plan is filled only by a managed route resolver. No header is a trusted plan.
// SourceURL/query mapping is supplied by the P3.2 route contract.
type Plan struct {
	FallbackToSource bool
	SourceURL        string
	Policy           transformv1.Policy
	Options          transformv1.RequestOptions
	AcceptHeader     http.Header
}

func (p Plan) Clone() Plan { return clonePlan(p) }

type planKey struct{}

func clonePlan(p Plan) Plan {
	p.Policy.Formats = slices.Clone(p.Policy.Formats)
	if p.Policy.PNGCompression != nil {
		v := *p.Policy.PNGCompression
		p.Policy.PNGCompression = &v
	}
	if p.Options.Quality != nil {
		v := *p.Options.Quality
		p.Options.Quality = &v
	}
	if p.Options.CompressionLevel != nil {
		v := *p.Options.CompressionLevel
		p.Options.CompressionLevel = &v
	}
	if p.Options.Lossless != nil {
		v := *p.Options.Lossless
		p.Options.Lossless = &v
	}
	p.AcceptHeader = p.AcceptHeader.Clone()
	return p
}
func WithPlan(ctx context.Context, p Plan) context.Context {
	return context.WithValue(ctx, planKey{}, clonePlan(p))
}
func PlanFromContext(ctx context.Context) (Plan, bool) {
	p, ok := ctx.Value(planKey{}).(Plan)
	return clonePlan(p), ok
}

// ClientHeaders owns the negotiation fields; arbitrary X-Tavern-* fields are
// deliberately absent. HTTP routing cannot elevate them into trusted state.
func ClientHeaders(req *http.Request) http.Header {
	result := make(http.Header)
	for key, values := range req.Header {
		if strings.EqualFold(key, "Accept") {
			result[key] = slices.Clone(values)
		}
	}
	return result
}

func WithoutPlan(ctx context.Context) context.Context { return context.WithValue(ctx, planKey{}, nil) }
