// countingWriter counts how many bytes have been written to it.
package multirange

import (
	"mime/multipart"

	"github.com/omalloc/tavern/pkg/x/http/rangecontrol"
)

type countingWriter int64

func (w *countingWriter) Write(p []byte) (n int, err error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

// rangesMIMESize returns the number of bytes it takes to encode the
// provided ranges as a multipart response.
func rangesMIMESize(ranges []rangecontrol.ByteRange, contentType string, contentSize uint64) (encSize int64) {
	var w countingWriter
	mw := multipart.NewWriter(&w)
	for _, ra := range ranges {
		mw.CreatePart(ra.MimeHeader(contentType, contentSize))
		encSize += ra.Length()
	}
	mw.Close()
	encSize += int64(w)
	return
}
