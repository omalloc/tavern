package caching

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/internal/constants"
	"github.com/omalloc/tavern/pkg/iobuf"
	xhttp "github.com/omalloc/tavern/pkg/x/http"
	"github.com/omalloc/tavern/proxy"
)

var cachingPool = sync.Pool{
	New: func() any {
		return &Caching{}
	},
}

type Caching struct {
	log          *log.Helper
	processor    *ProcessorChain
	opt          *cachingOption
	req          *http.Request
	id           *object.ID
	md           *object.Metadata
	bucket       storage.Bucket
	proxyClient  proxy.Proxy
	cacheStatus  storage.CacheStatus
	cacheable    bool
	hit          bool
	prefetch     bool
	revalidate   bool
	fileChanged  bool
	noContentLen bool // noContentLen indicates whether the content length is omitted in the HTTP response.
	migration    bool // cache migration
}

func (c *Caching) markCacheStatus(start, end int64) {
	if c.req.Method == http.MethodHead || end == math.MaxInt {
		if c.migration {
			c.cacheStatus = storage.CacheHotHit
		}
		return
	}

	first := uint32(start / iobuf.BitBlock)
	last := uint32(end / iobuf.BitBlock)

	// full hit
	if iobuf.FullHit(first, last, c.md.Parts) {
		c.cacheStatus = storage.CacheHit
		if c.migration {
			c.cacheStatus = storage.CacheHotHit
		}
		return
	}

	// part hit
	if iobuf.PartHit(first, last, c.md.Parts) {
		c.cacheStatus = storage.CachePartHit
		return
	}

	// part miss
	c.cacheStatus = storage.CachePartMiss
}

func (c *Caching) setXCache(resp *http.Response) {
	if resp == nil {
		return
	}

	resp.Header.Set(constants.ProtocolCacheStatusKey, strings.Join([]string{c.cacheStatus.String(), "from", c.opt.Hostname, "(tavern/4.0)"}, " "))

	// debug header
	if trace := c.req.Header.Get(constants.InternalTraceKey); trace != "" {
		resp.Header.Set(constants.InternalStoreUrl, strconv.FormatInt(int64(c.cacheStatus), 10))
		resp.Header.Set(constants.InternalSwapfile, c.id.WPath(c.bucket.Path()))
	}
}

func (c *Caching) reset() {
	c.cacheable = false
	c.hit = false
	c.prefetch = false
	c.revalidate = false
	c.fileChanged = false
	c.noContentLen = false
	c.migration = false
}

// ropen ReadOnly OpenFile
func ropen(wpath string) (*os.File, error) {
	return os.OpenFile(wpath, fileMode, 0o755)
}

// isTooManyFiles checks if the provided error is due to too many open files.
func isTooManyFiles(err error) bool {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return errors.Is(pathError.Err, syscall.EMFILE)
	}
	return errors.Is(err, syscall.EMFILE)
}

func cloneRequest(req *http.Request) *http.Request {
	proxyURL, _ := url.Parse(req.URL.String())
	if proxyURL.Host == "" {
		proxyURL.Host = req.Host
	}
	if proxyURL.Scheme == "" {
		proxyURL.Scheme = xhttp.Scheme(req)
	}
	proxyReq := &http.Request{
		ProtoMajor: req.ProtoMajor,
		ProtoMinor: req.ProtoMinor,
		Proto:      req.Proto,
		Host:       req.Host,
		Method:     req.Method,
		URL:        proxyURL,
		Header:     make(http.Header),
	}
	xhttp.CopyHeader(proxyReq.Header, req.Header)
	xhttp.RemoveHopByHopHeaders(proxyReq.Header)

	return proxyReq
}

func newObjectIDFromRequest(req *http.Request, vd string, includeQuery bool) (*object.ID, error) {
	// option: cache-key include querystring
	//
	// TODO: get cache-key from frontend protocol rule.

	// or later default rule.
	if includeQuery {
		return object.NewVirtualID(req.URL.String(), vd), nil
	}

	return object.NewVirtualID(fmt.Sprintf("%s://%s%s", req.URL.Scheme, req.Host, req.URL.Path), vd), nil
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
