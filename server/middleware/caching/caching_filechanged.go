package caching

import (
	"errors"
	"net/http"
	"strings"
	"time"

	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

// _ is a compile-time assertion to ensure FileChangedProcessor implements the Processor interface.
var _ Processor = (*FileChangedProcessor)(nil)

// FileChangeOption represents a functional option for configuring a FileChangedProcessor.
type FileChangeOption func(r *FileChangedProcessor)

// FileChangedProcessor handles the detection and processing of file changes during cache operations.
type FileChangedProcessor struct {
}

// Lookup determines the validity of a file change based on the provided caching mechanism and HTTP request.
func (r *FileChangedProcessor) Lookup(_ *Caching, _ *http.Request) (bool, error) {
	return true, nil
}

// PreRequest processes the HTTP request before it is sent, potentially modifying it, and returns the updated request.
func (r *FileChangedProcessor) PreRequest(_ *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

// PostRequest processes the HTTP response after it is received, checking for file changes and metadata mismatches.
// It validates content length, ETag, and Last-Modified headers, and handles discrepancies by updating the cache state.
func (r *FileChangedProcessor) PostRequest(c *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	if c.md != nil &&
		!c.revalidate &&
		!c.noContentLen &&
		!c.md.IsVary() {

		obj := c.md.Clone()
		flag := true

		rr, err := xhttp.ParseContentRange(resp.Header)
		if err != nil {
			rr = xhttp.ContentRange{ObjSize: c.md.Size}
		}

		if obj.Size != rr.ObjSize {
			flag = false
			c.fileChanged = true
			c.log.Warnf("FileChangedProcessor: cl not match, old: %d, new: %d, id: %s", obj.Size, rr.ObjSize, c.id.String())
			_ = c.bucket.DiscardWithMessage(c.req.Context(), c.id, "file changed with cl not match")
		}

		oldEtag := strings.ToLower(obj.Headers.Get("ETag"))
		newEtag := strings.ToLower(resp.Header.Get("ETag"))
		if flag && oldEtag != "" && newEtag != "" &&
			!strings.EqualFold(oldEtag, newEtag) {
			flag = false
			c.fileChanged = true
			c.log.Warnf("FileChangedProcessor: etag not match, old: %q, new: %q, id: %s", oldEtag, newEtag, c.id.String())
			_ = c.bucket.DiscardWithMessage(c.req.Context(), c.id, "file changed with etag not match")
		}

		objLastModified := obj.Headers.Get("Last-Modified")
		respLastModified := resp.Header.Get("Last-Modified")
		if flag && objLastModified != "" && respLastModified != "" {
			oldLm, err1 := parseLastModified(objLastModified)
			newLm, err2 := parseLastModified(respLastModified)
			if err1 != nil || err2 != nil || !oldLm.Equal(newLm) {
				c.fileChanged = true
				c.log.Warnf("FileChangedProcessor: last-modified not match, old: %s, new: %s, id: %s", oldLm, newLm, c.id.String())
				_ = c.bucket.DiscardWithMessage(c.req.Context(), c.id, "file changed with last-modified not match")
			}
		}
	}

	if c.fileChanged {
		if c.rootmd == nil {
			c.md.Chunks.Clear()
		}
	}

	return resp, nil
}

// NewFileChangedProcessor creates and returns a new instance of FileChangedProcessor with optional configuration options.
func NewFileChangedProcessor(opts ...FileChangeOption) Processor {
	p := &FileChangedProcessor{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// parseLastModified parses the Last-Modified HTTP header into a time.Time object using the RFC1123 format.
// It returns an error if the header cannot be parsed or is empty.
func parseLastModified(header string) (time.Time, error) {
	lm, err := time.Parse(time.RFC1123, header)
	if err != nil {
		return time.Time{}, errors.New("last-modified not found")
	}
	return lm, nil
}
