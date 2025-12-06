package http

import (
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/omalloc/tavern/pkg/x/http/cachecontrol"
)

// CopyHeader copies all headers from the source http.Header to the destination http.Header.
// It iterates over each header key-value pair in the source and adds them to the destination.
func CopyHeader(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = make([]string, 0, len(vv))
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// CopyTrailer copies all headers from the source http.Header to the destination http.Header,
// prefixing each header key with the http.TrailerPrefix. This function is useful for handling
// HTTP trailers, which are headers sent after the body of an HTTP message.
//
// see https://pkg.go.dev/net/http#example-ResponseWriter-Trailers
//
// - dst: The destination http.Header where the headers will be copied to.
// - src: The source http.Header from which the headers will be copied.
//
// Example usage:
//
//	src := http.Header{
//	    "Example-Key": {"Example-Value"},
//	}
//	dst := http.Header{}
//	CopyTrailer(dst, src)
//	// dst will now contain "Trailer-Example-Key": {"Example-Value"}
func CopyTrailer(dst, src http.Header) {
	for k, v := range src {
		dst[http.TrailerPrefix+k] = slices.Clone(v)
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.hop-by-hop headers
// As of RFC 7230, hop-by-hop headers are required to appear in the
// Connection header field. These are the headers defined by the
// obsoleted RFC 2616 (section 13.5.1) and are used for backward
// compatibility.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",      // canonicalized version of "TE"
	"Trailer", // not Trailers per URL above; https://www.rfc-editor.org/errata_search.php?eid=4522
	"Transfer-Encoding",
	"Upgrade",
}

// RemoveHopByHopHeaders removes hop-by-hop headers.
func RemoveHopByHopHeaders(h http.Header) {
	// RFC 7230, section 6.1: Remove headers listed in the "Connection" header.
	for _, f := range h["Connection"] {
		for _, sf := range strings.Split(f, ",") {
			if sf = textproto.TrimString(sf); sf != "" {
				h.Del(sf)
			}
		}
	}
	// RFC 2616, section 13.5.1: Remove a set of known hop-by-hop headers.
	// This behavior is superseded by the RFC 7230 Connection header, but
	// preserve it for backwards compatibility.
	for _, f := range hopHeaders {
		h.Del(f)
	}
}

// IsChunked checks if the Transfer-Encoding header is chunked.
//
// see https://www.rfc-editor.org/rfc/rfc9112.html#name-chunked-transfer-coding
func IsChunked(h http.Header) bool {
	return h.Get("Transfer-Encoding") == "chunked" || h.Get("Content-Length") == ""
}

const DefaultProtocolCacheTime = time.Second * 300

// ParseCacheTime parses cache time from HTTP headers.
//
// If withKey is empty, it will parse from standard Cache-Control and Expires headers.
// If withKey is provided, it will look for that specific header key to determine cache time.
//
// It returns the parsed cache duration and a boolean indicating whether caching is allowed.
func ParseCacheTime(withKey string, src http.Header) (time.Duration, bool) {
	if withKey == "" {
		hcc := src.Get("Cache-Control")
		expire := src.Get("Expires")

		if hcc == "" && expire == "" {
			return DefaultProtocolCacheTime, true
		}

		ctrl := cachecontrol.Parse(hcc)

		if ctrl.MaxAge() > 0 {
			return ctrl.MaxAge(), true
		}

		if expire != "" {
			if t, err := time.Parse(time.RFC1123, expire); err == nil {
				// use the server time from the Date header to calculate
				return time.Until(t), true
			}
		}

		if !ctrl.Cacheable() {
			return 0, false
		}

		// default cache time
		return time.Second * 3600, true
	}

	str := src.Get(withKey)

	ct, _ := strconv.Atoi(str)
	// No-Cache
	if ct <= 0 {
		return 0, false
	}

	return time.Duration(ct) * time.Second, true
}
