package mod

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/omalloc/tavern/pkg/traces"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
)

const layout = "[02/Jan/2006:15:04:05 -0700]"

func WithNormalFields(req *http.Request, resp *xhttp.ResponseRecorder) []byte {
	tr := traces.FromContext(req.Context())

	buf := NewFieldBuffer(' ')

	// 1. client-ip
	buf.Append(xhttp.ClientIP(req.RemoteAddr, req.Header))
	// 2. domain
	buf.Append(req.URL.Hostname())
	// 3. content-type
	buf.FAppend(resp.Header().Get("Content-Type"))
	// 4+5. request time
	buf.Append(time.Now().Format(layout))
	// 6. request method
	buf.FAppend(fmt.Sprintf("%s %s %s", req.Method, req.URL, req.Proto))
	// 7. response status
	buf.Append(strconv.Itoa(resp.Status()))
	// 8. sent bytes (header + body)
	buf.Append(strconv.FormatUint(bytesSent(resp), 10))
	// 9. referer
	buf.FAppend(req.Header.Get("Referer"))
	// 10. user-agent
	buf.FAppend(req.Header.Get("User-Agent"))
	// 11. response time (ms)
	buf.Append(strconv.FormatInt(time.Since(tr.StartAt).Milliseconds(), 10))
	// 12. response body size
	buf.Append(strconv.FormatUint(resp.Size(), 10))
	// 13. content-length
	buf.FAppend(req.Header.Get("Content-Length"))
	// 14. request range header
	buf.FAppend(req.Header.Get("Range"))
	// 15. x-forwarded-for
	buf.FAppend(req.Header.Get("X-Forwarded-For"))
	// 16. cache status
	buf.Append(tr.CacheStatus)
	// 17. request-id
	buf.Append(tr.RequestID)

	return buf.Bytes()
}

func bytesSent(resp *xhttp.ResponseRecorder) uint64 {
	return xhttp.ResponseHeaderSize(resp.Status(), resp.Header()) + resp.Size()
}
