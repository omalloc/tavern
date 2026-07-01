package rangecontrol

import (
	"errors"
	"fmt"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrRangeHeaderNotFound = errors.New("header Range not found")
	ErrInvalidRange        = errors.New("invalid Range header")
)

type ByteRange struct {
	Start int64 // inclusive
	End   int64 // inclusive, -1 表示 open-ended (100-)
}

func (r ByteRange) Length() int64 {
	return r.End - r.Start + 1
}

func (r ByteRange) String() string {
	if r.End <= 0 && r.Start > 0 {
		return fmt.Sprintf("bytes=%d-", r.Start)
	}
	return fmt.Sprintf("bytes=%d-%d", r.Start, r.End)
}

func (r ByteRange) ContentRange(totalSize uint64) string {
	if totalSize <= 0 {
		return fmt.Sprintf("bytes %d-%d/*", r.Start, r.End)
	}

	if r.End == -1 || r.End >= int64(totalSize) {
		return fmt.Sprintf("bytes %d-%d/%d", r.Start, totalSize-1, totalSize)
	}

	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, totalSize)
}

func (r ByteRange) MimeHeader(contentType string, size uint64) textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Range": {r.ContentRange(size)},
		"Content-Type":  {contentType},
	}
}

// Parse parses a Range header and returns.
func Parse(rangeHeader string, totalSize uint64) ([]ByteRange, error) {
	if rangeHeader == "" {
		return nil, nil
	}

	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return nil, ErrInvalidRange
	}

	raw := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.Split(raw, ",")

	ranges := make([]ByteRange, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)

		dash := strings.IndexByte(part, '-')
		if dash < 0 {
			return nil, ErrInvalidRange
		}

		startStr := part[:dash]
		endStr := part[dash+1:]

		// suffix-range: "-500" (not supported without total size)
		if strings.HasPrefix(part, "-") {
			end, err := strconv.ParseInt(endStr, 10, 64)
			if err != nil || end < 0 {
				return nil, ErrInvalidRange
			}

			ranges = append(ranges, ByteRange{
				Start: int64(totalSize) - end,
				End:   int64(totalSize) - 1,
			})
			continue
		}

		start, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 {
			return nil, ErrInvalidRange
		}

		var end int64
		if endStr == "" {
			// open-ended range: "100-"
			// end = -1
			end = int64(totalSize) - 1
		} else {
			end, err = strconv.ParseInt(endStr, 10, 64)
			if err != nil || end < start {
				return nil, fmt.Errorf("open-ended range format err %s-%s", startStr, endStr)
			}
		}

		ranges = append(ranges, ByteRange{
			Start: start,
			End:   end,
		})
	}

	return ranges, nil
}

var (
	// ErrNoOverlap is returned by ParseRange if first-byte-pos of
	// all of the byte-range-spec values is greater than the content size.
	ErrNoOverlap = errors.New("invalid range: failed to overlap")

	// ErrInvalid is returned by ParseRange on invalid input.
	ErrInvalid = errors.New("invalid range")
)

// ParseRange parses a Range header string as per RFC 7233.
// ErrNoOverlap is returned if none of the ranges overlap.
// ErrInvalid is returned if s is invalid range.
func ParseRange(s string, size int64) ([]ByteRange, error) { // nolint:gocognit
	if s == "" {
		return nil, nil // header not present
	}
	const b = "bytes="
	if !strings.HasPrefix(s, b) {
		return nil, ErrInvalid
	}
	var ranges []ByteRange
	noOverlap := false
	for _, ra := range strings.Split(s[len(b):], ",") {
		ra = textproto.TrimString(ra)
		if ra == "" {
			continue
		}
		i := strings.Index(ra, "-")
		if i < 0 {
			return nil, ErrInvalid
		}
		start, end := textproto.TrimString(ra[:i]), textproto.TrimString(ra[i+1:])
		var r ByteRange
		if start == "" {
			// If no start is specified, end specifies the
			// range start relative to the end of the file,
			// and we are dealing with <suffix-length>
			// which has to be a non-negative integer as per
			// RFC 7233 Section 2.1 "Byte-Ranges".
			if end == "" || end[0] == '-' {
				return nil, ErrInvalid
			}
			i, err := strconv.ParseInt(end, 10, 64)
			if i < 0 || err != nil {
				return nil, ErrInvalid
			}
			if i > size {
				i = size
			}
			r.Start = size - i
			r.End = size - r.Start
		} else {
			i, err := strconv.ParseInt(start, 10, 64)
			if err != nil || i < 0 {
				return nil, ErrInvalid
			}
			if i >= size {
				// If the range begins after the size of the content,
				// then it does not overlap.
				noOverlap = true
				continue
			}
			r.Start = i
			if end == "" {
				// If no end is specified, range extends to end of the file.
				r.End = size - r.Start
			} else {
				i, err := strconv.ParseInt(end, 10, 64)
				if err != nil || r.Start > i {
					return nil, ErrInvalid
				}
				if i >= size {
					i = size - 1
				}
				r.End = i - r.Start + 1
			}
		}
		ranges = append(ranges, r)
	}
	if noOverlap && len(ranges) == 0 {
		// The specified ranges did not overlap with the content.
		return nil, ErrNoOverlap
	}
	return ranges, nil
}

func SortRanges(ranges []ByteRange) {
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}

		// same start, closed range first
		if ranges[i].End == -1 {
			return false
		}
		if ranges[j].End == -1 {
			return true
		}
		return ranges[i].End < ranges[j].End
	})
}

func MergeRanges(ranges []ByteRange) []ByteRange {
	if len(ranges) == 0 {
		return ranges
	}

	SortRanges(ranges)

	merged := make([]ByteRange, 0, len(ranges))
	cur := ranges[0]

	for i := 1; i < len(ranges); i++ {
		next := ranges[i]

		// open-ended absorbs everything
		if cur.End == -1 {
			break
		}

		// overlapping or adjacent
		if next.Start <= cur.End+1 {
			if next.End == -1 || next.End > cur.End {
				cur.End = next.End
			}
		} else {
			merged = append(merged, cur)
			cur = next
		}
	}

	merged = append(merged, cur)
	return merged
}
