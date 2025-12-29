package caching

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/omalloc/tavern/internal/constants"
	"github.com/omalloc/tavern/pkg/iobuf"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

var _ Processor = (*fillRange)(nil)

type fillRangeKey struct{}
type fillRangeContext struct {
	newStart int
	newEnd   int
	rawStart int
	rawEnd   int
	flag     bool
}
type FillRangeOption func(f *fillRange)

type fillRange struct {
	fillRangePercent uint64
	chunkSize        uint64
}

func NewFillRangeProcessor(opts ...FillRangeOption) Processor {
	f := &fillRange{
		fillRangePercent: 100,
		chunkSize:        1048576, // 1MB
	}

	for _, opt := range opts {
		opt(f)
	}

	return f
}

// Lookup implements [Processor].
func (f *fillRange) Lookup(c *Caching, req *http.Request) (bool, error) {
	return true, nil
}

// PreRequest implements [Processor].
func (f *fillRange) PreRequest(c *Caching, req *http.Request) (*http.Request, error) {
	rawRange := req.Header.Get("Range")
	if rawRange == "" || f.fillRangePercent == 0 {
		return req, nil
	}

	return f.fill(c, req, rawRange), nil
}

// PostRequest implements [Processor].
func (f *fillRange) PostRequest(c *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	frk := req.Context().Value(fillRangeKey{})
	if frk != nil && resp.StatusCode == http.StatusPartialContent {
		fill := frk.(*fillRangeContext)
		if !fill.flag {
			return resp, nil
		}

		// adjust Content-Range header
		cr, err := xhttp.ParseContentRange(resp.Header)
		if err != nil {
			c.log.Warnf("parse content-range error: %v; origin value: %q; meta: %v; req.header: %v", err, resp.Header.Get("Content-Range"), c.md, req.Header)
			return resp, err
		}

		c.log.Infof("parse content-range value %v", cr)

		// 请求的 Range 范围超出文件大小; 中断响应
		if cr.ObjSize < uint64(fill.rawStart) {
			c.log.Warnf("range %s out of file-size: %d", fmt.Sprintf("bytes=%d-%d", fill.rawStart, fill.rawEnd), cr.ObjSize)
			headers := make(http.Header)
			xhttp.CopyHeadersWithout(headers, resp.Header, "Content-Length", "Content-Range")
			headers.Set("Content-Range", fmt.Sprintf("bytes */%d", cr.ObjSize))
			// 关闭 body 防止 tcp 泄露
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			// return xhttp.NewBizResponse(http.StatusRequestedRangeNotSatisfiable, headers, resp)
			return nil, xhttp.NewBizError(http.StatusRequestedRangeNotSatisfiable, headers)
		}

		// 修正填充的 end 数据范围
		newEnd := (fill.newEnd - fill.newStart)
		if newEnd > int(resp.ContentLength) {
			fill.newEnd = fill.newStart + int(resp.ContentLength) - 1
		}
		if fill.rawEnd > fill.newEnd {
			fill.rawEnd = fill.newEnd
		}

		resp.Body = iobuf.RangeReader(resp.Body, fill.newStart, fill.newEnd, fill.rawStart, fill.rawEnd)

		resp.Header.Set("Content-Range", xhttp.BuildHeaderRange(uint64(fill.rawStart), uint64(fill.rawEnd), uint64(cr.ObjSize)))
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", fill.rawEnd-fill.rawStart+1))
	}
	return resp, nil
}

func (f *fillRange) fill(c *Caching, req *http.Request, rawRange string) *http.Request {
	objSize := uint64(math.MaxInt64) // default value, set to MaxInt64
	if c.md != nil {
		objSize = c.md.Size
	}

	rng, err := xhttp.SingleRange(rawRange, objSize)
	if err != nil {
		// unable to parse range, return original request
		return req
	}

	fill := &fillRangeContext{
		flag:     false,
		newStart: 0,
		newEnd:   0,
		rawStart: 0,
		rawEnd:   0,
	}

	fp := parseFillPercent(req.Header, f.fillRangePercent)

	maxFillSize := f.chunkSize * fp / 100
	minFillSize := f.chunkSize * (100 - fp) / 100

	// Range Start
	fill.newStart = int((uint64(rng.Start) / f.chunkSize) * f.chunkSize)
	fill.rawStart = int(rng.Start)
	if fill.rawStart-fill.newStart > int(maxFillSize) {
		fill.newStart = fill.rawStart
	}

	// Range End
	fill.newEnd = int((uint64(rng.End)/f.chunkSize+1)*f.chunkSize - 1)
	fill.rawEnd = int(rng.End)
	if fill.newEnd-fill.rawEnd > int(maxFillSize) {
		fill.newEnd = fill.rawEnd
	}

	// check validity
	if fill.rawStart < 0 || fill.rawEnd < 0 {
		return req
	}
	if (fill.rawEnd >= 0 && fill.rawEnd < fill.rawStart) || (fill.newEnd >= 0 && fill.newEnd < fill.newStart) {
		return req
	}
	if (fill.newEnd-fill.newStart <= int(f.chunkSize) && fill.rawStart-fill.newStart+fill.newEnd-fill.rawEnd > int(maxFillSize)) ||
		(fill.rawEnd-fill.rawStart+1) < int(minFillSize) {
		fill.newStart = fill.rawStart
		fill.newEnd = fill.rawEnd
	}

	var ns, ne string
	if fill.newStart >= 0 {
		ns = fmt.Sprintf("%d", fill.newStart)
	}
	if fill.newEnd >= 0 {
		ne = fmt.Sprintf("%d", fill.newEnd)
	}

	newRange := fmt.Sprintf("bytes=%s-%s", ns, ne)
	req.Header.Set("Range", newRange)
	if fill.newStart != fill.rawStart || fill.newEnd != fill.rawEnd {
		fill.flag = true
	}

	c.log.Debugf("fill-mode objSize=%d, rawRange=%v, fillRange=%s", objSize, rng, newRange)

	return req.WithContext(context.WithValue(req.Context(), fillRangeKey{}, fill))
}

func parseFillPercent(h http.Header, def uint64) uint64 {
	fp := h.Get(constants.InternalFillRangePercent)
	if fp != "" {
		p, err := strconv.ParseUint(fp, 10, 64)
		if err != nil {
			return 0
		}

		if p <= 0 || p > 100 {
			return 0
		}
		return p
	}
	return def
}

func WithFillRangePercent(fillRangePercent int) FillRangeOption {
	return func(f *fillRange) {
		f.fillRangePercent = uint64(fillRangePercent)
	}
}

func WithChunkSize(chunkSize uint64) FillRangeOption {
	return func(f *fillRange) {
		f.chunkSize = chunkSize
	}
}
