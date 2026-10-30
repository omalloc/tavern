package caching

import (
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/pkg/x/http/rangecontrol"
)

// representationSpan describes origin bytes independently of the headers sent
// to the client and the full representation headers persisted with the body.
type representationSpan struct {
	start, length uint64
	unknown       bool
}

func representationRequestRange(raw string, size uint64) (*xhttp.Range, error) {
	if size == 0 || size > math.MaxInt64 {
		return nil, rangecontrol.ErrInvalidRange
	}
	if raw == "" {
		return xhttp.NewRequestRange(0, int64(size)-1), nil
	}
	ranges, err := rangecontrol.ParseRange(raw, int64(size))
	if err != nil || len(ranges) != 1 || ranges[0].End <= 0 {
		return nil, rangecontrol.ErrInvalidRange
	}
	// ParseRange's End is the resolved length (including suffix/open ranges).
	return xhttp.NewRequestRange(ranges[0].Start, ranges[0].Start+ranges[0].End-1), nil
}

// Cropping sits outside the origin writer. Consume the full origin response
// before finishing the requested tail so EOF can commit the new generation.
// Early client Close still aborts the session rather than draining in the
// background with the client's request context.
func applyRepresentationRange(req *http.Request, resp *http.Response) (*http.Response, error) {
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return resp, nil
	}
	rng, err := representationRequestRange(req.Header.Get("Range"), uint64(resp.ContentLength))
	if err != nil {
		headers := resp.Header.Clone()
		headers.Del("Content-Length")
		headers.Set("Content-Range", "bytes */"+strconv.FormatInt(resp.ContentLength, 10))
		closeBody(resp)
		return nil, xhttp.NewBizError(http.StatusRequestedRangeNotSatisfiable, headers)
	}
	resp.Header = resp.Header.Clone()
	resp.Header.Set("Content-Range", rng.ContentRange(uint64(resp.ContentLength)))
	resp.ContentLength = rng.Length()
	resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	resp.StatusCode, resp.Status = http.StatusPartialContent, "206 Partial Content"
	resp.Body = &representationRangeReader{body: resp.Body, skip: rng.Start, remaining: rng.Length()}
	return resp, nil
}

type representationRangeReader struct {
	body            io.ReadCloser
	skip, remaining int64
}

var _ io.ReadCloser = (*representationRangeReader)(nil)

func (r *representationRangeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if r.skip > 0 {
		n, err := io.CopyN(io.Discard, r.body, r.skip)
		r.skip -= n
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
	}
	n, err := r.body.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	if r.remaining == 0 && (err == nil || err == io.EOF) {
		if _, drainErr := io.Copy(io.Discard, r.body); drainErr != nil {
			return n, drainErr
		}
		return n, io.EOF
	}
	return n, err
}

func (r *representationRangeReader) Close() error { return r.body.Close() }

func analyzeRepresentationResponse(req *http.Request, resp *http.Response, blockSize uint64) (http.Header, uint64, representationSpan, bool) {
	if resp.Body == nil || blockSize == 0 || blockSize > math.MaxInt64 || len(resp.Trailer) != 0 {
		return nil, 0, representationSpan{}, false
	}
	headers := resp.Header.Clone()
	xhttp.RemoveHopByHopHeaders(headers)
	span := representationSpan{}
	var size uint64
	switch resp.StatusCode {
	case http.StatusOK:
		if headers.Get("Content-Range") != "" {
			return nil, 0, span, false
		}
		if resp.ContentLength < 0 && len(headers.Values("Content-Length")) == 0 {
			span.unknown = true
			return headers, 0, span, true
		}
		length, err := strconv.ParseUint(headers.Get("Content-Length"), 10, 63)
		if err != nil || length == 0 || int64(length) != resp.ContentLength || len(headers.Values("Content-Length")) != 1 {
			return nil, 0, span, false
		}
		size, span.length = length, length
	case http.StatusPartialContent:
		// A single, solicited byte range with a known total is required. Each
		// response gets a new generation, including when its validator changed.
		if req.Header.Get("Range") == "" || strings.Contains(req.Header.Get("Range"), ",") ||
			len(headers.Values("Content-Range")) != 1 || !strings.HasPrefix(headers.Get("Content-Range"), "bytes ") {
			return nil, 0, span, false
		}
		rng, err := rangecontrol.ParseContentRange(headers.Get("Content-Range"))
		if err != nil || rng.Unsatisfied || rng.Size <= 0 {
			return nil, 0, span, false
		}
		requested, err := representationRequestRange(req.Header.Get("Range"), uint64(rng.Size))
		if err != nil || requested.Start != rng.Start || requested.End != rng.End {
			return nil, 0, span, false
		}
		length, err := strconv.ParseUint(headers.Get("Content-Length"), 10, 63)
		if err != nil || len(headers.Values("Content-Length")) != 1 || int64(length) != resp.ContentLength ||
			length != uint64(rng.End-rng.Start+1) {
			return nil, 0, span, false
		}
		size, span.start, span.length = uint64(rng.Size), uint64(rng.Start), length
		headers.Del("Content-Range")
		headers.Set("Content-Length", strconv.FormatUint(size, 10))
	default:
		return nil, 0, span, false
	}
	if (size-1)/blockSize > math.MaxUint32 {
		return nil, 0, span, false
	}
	return headers, size, span, true
}
