package caching

import (
	"net/http"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
)

func TestFileChangedETagIsOpaqueAndCaseSensitive(t *testing.T) {
	_, bucket, target := newVaryTestCache(t, func(http.ResponseWriter, *http.Request) {})
	for _, newTag := range []string{`"Version-A"`, `"version-a"`, `W/"Version-A"`} {
		req := varyRequest(t, target, nil)
		id := object.NewID(target)
		md := &object.Metadata{ID: id, Headers: http.Header{"Etag": {`"Version-A"`}}, Size: 3, BlockSize: 64}
		md.Chunks.Set(0)
		c := &Caching{req: req, id: id, md: md, bucket: bucket, log: log.Context(req.Context())}
		response := &http.Response{StatusCode: 200, Header: http.Header{"Etag": {newTag}, "Content-Length": {"3"}}}
		if _, err := NewFileChangedProcessor().PostRequest(c, req, response); err != nil {
			t.Fatal(err)
		}
		changed := newTag != `"Version-A"`
		if c.fileChanged != changed || md.Chunks.Contains(0) == changed {
			t.Fatalf("opaque ETag %s changed=%v chunks=%v", newTag, c.fileChanged, md.Chunks)
		}
	}
}
