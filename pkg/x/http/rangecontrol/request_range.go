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
func Parse(rangeHeader string) ([]ByteRange, error) {
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

		// suffix-range: "-500" (not supported without total size)
		if strings.HasPrefix(part, "-") {
			return nil, ErrInvalidRange
		}

		dash := strings.IndexByte(part, '-')
		if dash < 0 {
			return nil, ErrInvalidRange
		}

		startStr := part[:dash]
		endStr := part[dash+1:]

		start, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 {
			return nil, ErrInvalidRange
		}

		var end int64
		if endStr == "" {
			// open-ended range: "100-"
			end = -1
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
