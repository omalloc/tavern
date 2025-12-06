package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// Hypertext Transfer Protocol (HTTP/1.1): Range Requests
// https://www.rfc-editor.org/rfc/rfc7233.html
const b = "bytes="

var (
	ErrContentRangeHeaderNotFound    = errors.New("Content-Range header not found")
	ErrContentRangeInvalidFormat     = errors.New("Content-Range invalid format")
	ErrContentRangeInvalidStartValue = errors.New("Content-Range invalid start value")
	ErrContentRangeInvalidEndValue   = errors.New("Content-Range invalid end value")
	ErrContentRangeInvalidTotalValue = errors.New("Content-Range invalid total value")
	ErrRangeHeaderNotFound           = errors.New("header Range not found")
	ErrRangeHeaderInvalidFormat      = errors.New("header Range invalid format")
	ErrRangeHeaderUnsatisfiable      = errors.New("header Range unsatisfiable")
)

type Range struct {
	Start, End int64
}

func (r *Range) Length() int64 {
	return r.End - r.Start + 1
}

func (r *Range) ContentRange(size uint64) string {
	if r.End <= 0 && r.Start > 0 {
		return fmt.Sprintf("bytes %d-%d/%d", r.Start, size-1, size)
	}
	end := r.End
	if end >= int64(size) {
		end = int64(size) - 1
	}
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, end, size)
}

func (r *Range) RangeLength(totalSize int64) int64 {
	if r.End <= 0 && r.Start > 0 {
		return int64(totalSize - r.Start)
	}
	return int64(r.End - r.Start + 1)
}

func (r *Range) MimeHeader(contentType string, size uint64) textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Range": {r.ContentRange(size)},
		"Content-Type":  {contentType},
	}
}

func (r *Range) String() string {
	if r.End <= 0 && r.Start > 0 {
		return fmt.Sprintf("bytes=%d-", r.Start)
	}
	return fmt.Sprintf("bytes=%d-%d", r.Start, r.End)
}

// BuildRange builds the Range header value.
func BuildHeaderRange(start, end, totalSize uint64) string {
	return fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize)
}

type Parser interface {
	Parse(header string, size uint64) ([]*Range, error)
	SingleParse(header string, size uint64) (*Range, error)
}

type ContentRange struct {
	Start   int64
	Length  int64
	ObjSize uint64
}

func (cr *ContentRange) String() string {
	if cr.Start == 0 && cr.Length == 0 {
		return fmt.Sprintf("bytes */%d", cr.ObjSize)
	}
	return fmt.Sprintf("bytes %d-%d/%d", cr.Start, cr.Length, cr.ObjSize)
}

type unsatisfiableParser struct{}

// Parse implements Parser.
func (u *unsatisfiableParser) Parse(header string, size uint64) ([]*Range, error) {
	if header == "" {
		return nil, ErrRangeHeaderNotFound
	}

	if !strings.HasPrefix(header, b) {
		return nil, ErrRangeHeaderInvalidFormat
	}

	index := strings.Index(header, "=")
	if index == -1 {
		return nil, ErrRangeHeaderInvalidFormat
	}

	size64 := int64(size)
	multipart := strings.Split(header[index+1:], ",")
	ranges := make([]*Range, 0, len(multipart))

	for _, part := range multipart {

		part = strings.TrimSpace(part)
		// not -nnn or nnn- or nnn-nnn
		if !strings.Contains(part, "-") {
			continue
		}

		r := strings.Split(part, "-")
		if len(r) < 2 {
			// -nnn
			if strings.HasPrefix(part, "-") {
				end, endErr := strconv.ParseInt(r[1], 10, 64)
				if endErr != nil {
					continue
				}

				ranges = append(ranges, &Range{
					Start: size64 - end,
					End:   size64 - 1,
				})
				continue
			}

			// nnn-
			if strings.HasSuffix(part, "-") {
				start, startErr := strconv.ParseInt(r[0], 10, 64)
				if startErr != nil {
					continue
				}

				ranges = append(ranges, &Range{
					Start: start,
					End:   size64 - 1,
				})
				continue
			}
			continue
		}

		start, startErr := strconv.ParseInt(r[0], 10, 64)
		end, endErr := strconv.ParseInt(r[1], 10, 64)

		if startErr != nil && endErr != nil {
			continue
		}

		// -nnn and nnn-
		if startErr != nil {
			start = size64 - end
			end = size64 - 1
		} else if endErr != nil {
			end = size64 - 1
		}

		if end >= size64 {
			end = size64 - 1
		}

		if start > end || start < 0 {
			continue
		}

		ranges = append(ranges, &Range{
			Start: start,
			End:   end,
		})
	}

	if len(ranges) == 0 {
		return nil, ErrRangeHeaderInvalidFormat
	}

	return ranges, nil
}

// SingleParse implements Parser.
func (u *unsatisfiableParser) SingleParse(header string, size uint64) (*Range, error) {
	rngs, err := u.Parse(header, size)
	if err != nil && errors.Is(err, ErrRangeHeaderNotFound) {
		if size == 0 {
			return &Range{
				Start: 0,
				End:   0,
			}, nil
		}

		return &Range{
			Start: 0,
			End:   int64(size) - 1,
		}, nil
	}
	if len(rngs) > 0 {
		return rngs[0], nil
	}
	return nil, ErrRangeHeaderInvalidFormat
}

func NewUnsatisfiableParser() Parser {
	return &unsatisfiableParser{}
}

// ParseContentRange parses the Content-Range header from an HTTP response.
func ParseContentRange(header http.Header) (*ContentRange, error) {
	cr := &ContentRange{}

	contentRange := header.Get("Content-Range")

	if contentRange == "" {
		cl, err := strconv.ParseUint(header.Get("Content-Length"), 10, 64)
		if err != nil {
			return cr, ErrContentRangeInvalidTotalValue
		}
		cr.ObjSize = cl
		return cr, nil
	}

	// e.g. Content-Range: "bytes 200-1000/67589"
	parts := strings.Split(contentRange, " ")
	if len(parts) != 2 {
		return cr, ErrContentRangeInvalidFormat
	}

	rangeParts := strings.Split(parts[1], "/")
	if len(rangeParts) != 2 {
		return cr, ErrContentRangeInvalidFormat
	}

	rangeValues := strings.Split(rangeParts[0], "-")
	if len(rangeValues) != 2 {
		return cr, ErrContentRangeInvalidFormat
	}

	_, err := fmt.Sscanf(rangeValues[0], "%d", &cr.Start)
	if err != nil {
		return cr, ErrContentRangeInvalidStartValue
	}

	_, err = fmt.Sscanf(rangeValues[1], "%d", &cr.Length)
	if err != nil {
		return cr, ErrContentRangeInvalidEndValue
	}

	_, err = fmt.Sscanf(rangeParts[1], "%d", &cr.ObjSize)
	if err != nil {
		cl, err1 := strconv.ParseUint(header.Get("Content-Length"), 10, 64)
		if err1 != nil {
			return cr, ErrContentRangeInvalidTotalValue
		}
		cr.ObjSize = cl
		return cr, nil
	}

	return cr, nil
}
