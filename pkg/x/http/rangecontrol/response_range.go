package rangecontrol

import (
	"errors"
	"strconv"
	"strings"
)

// Hypertext Transfer Protocol (HTTP/1.1): Range Requests
// RFC 9110
//
// Content-Range = unit SP ( range-resp / unsatisfied-range )
// range-resp    = first-byte-pos "-" last-byte-pos "/" ( complete-length / "*" )
// unsatisfied   = "*" "/" complete-length
//
// Content-Range: bytes 0-99/1000
// Content-Range: bytes 200-299/*
// Content-Range: bytes */1000

const b = "bytes="

var (
	ErrInvalidContentRange = errors.New("invalid Content-Range")
)

type ContentRange struct {
	Start       int64 // >=0, undefined if Unsatisfied
	End         int64 // >=Start
	Size        int64 // total size, -1 if unknown
	Unsatisfied bool  // true for "*/size"
}

func ParseContentRange(header string) (*ContentRange, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, ErrInvalidContentRange
	}

	// Split unit and value
	sp := strings.IndexByte(header, ' ')
	if sp < 0 {
		return nil, ErrInvalidContentRange
	}

	value := header[sp+1:]

	// unsatisfied-range: */size
	if strings.HasPrefix(value, "*/") {
		sizeStr := strings.TrimPrefix(value, "*/")
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil || size < 0 {
			return nil, ErrInvalidContentRange
		}

		return &ContentRange{
			Size:        size,
			Unsatisfied: true,
		}, nil
	}

	// range-resp: start-end/size or start-end/*
	slash := strings.IndexByte(value, '/')
	if slash < 0 {
		return nil, ErrInvalidContentRange
	}

	rangePart := value[:slash]
	sizePart := value[slash+1:]

	dash := strings.IndexByte(rangePart, '-')
	if dash < 0 {
		return nil, ErrInvalidContentRange
	}

	startStr := rangePart[:dash]
	endStr := rangePart[dash+1:]

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return nil, ErrInvalidContentRange
	}

	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return nil, ErrInvalidContentRange
	}

	var size int64
	if sizePart == "*" {
		size = -1
	} else {
		size, err = strconv.ParseInt(sizePart, 10, 64)
		if err != nil || size <= 0 || end >= size {
			return nil, ErrInvalidContentRange
		}
	}

	return &ContentRange{
		Start: start,
		End:   end,
		Size:  size,
	}, nil
}
