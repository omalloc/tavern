package caching

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
	rootmd       *object.Metadata
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

func (c *Caching) hasNoCache() bool {
	return c.md == nil
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

func (c *Caching) getAvailableChunks() (available []uint32) {
	available = make([]uint32, 0, c.md.Chunks.Count())
	c.md.Chunks.Range(func(x uint32) {
		available = append(available, x)
	})
	return available
}

func getContents(c *Caching, reqChunks []uint32, from uint32) (reader io.ReadCloser, count int, err error) {
	idx := reqChunks[from]
	partSize := c.opt.SliceSize
	if c.md.BlockSize > 0 {
		partSize = uint64(c.md.BlockSize)
	}

	// find the first existing chunk
	f, err1 := getChunkSlice(c, idx)
	if err1 != nil {
		return nil, 0, err1
	}

	if f != nil {
		// check file size
		if err = checkChunkSize(c, f, idx); err != nil {
			return f, 1, nil
		}
	}

	// find all hit block.
	availableChunks := c.getAvailableChunks()
	slices.SortFunc(availableChunks, func(i, j uint32) int {
		return int(availableChunks[i] - availableChunks[j])
	})
	index := sort.Search(len(availableChunks), func(i int) bool {
		return availableChunks[i] > reqChunks[from] &&
			availableChunks[i] <= reqChunks[len(reqChunks)-1]
	})

	fromByte := uint64(reqChunks[from] * uint32(c.md.BlockSize))
	if index < len(availableChunks) {
		chunk, _ := getChunkSlice(c, availableChunks[index])
		if chunk != nil {
			if err := checkChunkSize(c, f, idx); err != nil {
				_ = c.bucket.Discard(context.Background(), c.id)
				return nil, 0, err
			}

			toByte := min(c.md.Size-1, uint64(availableChunks[index]*uint32(partSize))-1)
			req := c.req.Clone(context.Background())
			newRange := fmt.Sprintf("bytes=%d-%d", fromByte, toByte)
			req.Header.Set("Range", newRange)

			reader := iobuf.AsyncReadCloser(func() (*http.Response, error) {
				now := time.Now()
				c.log.Debugf("doProxy[middle]: begin: %s, Range: %s", now, newRange)
				resp, err1 := c.doProxy(req, true)
				c.log.Debugf("doProxy[middle]: timeCost: %s, Range: %s", time.Since(now), newRange)
				if err1 != nil {
					return nil, err
				}

				// 发起的是 206 请求，但是返回的非 206
				if resp.StatusCode != http.StatusPartialContent {
					c.log.Warnf("doProxy[middle]: status code: %d, bod size: %d", resp.StatusCode, resp.ContentLength)
					return resp, xhttp.NewBizError(resp.StatusCode, resp.Header)
				}
				return resp, err1
			})

			return iobuf.PartsReader(chunk /* io */, reader, chunk), int(availableChunks[index]-availableChunks[from]) + 1, nil
		}
	}

	// no more hit block, fill
	toByte := min(c.md.Size-1, uint64(reqChunks[len(reqChunks)-1]+1)*uint64(partSize)-1)
	rawRange := c.req.Header.Get("Range")
	newRange := fmt.Sprintf("bytes=%d-%d", fromByte, toByte)
	req := c.req.Clone(context.Background())
	req.Header.Set("Range", newRange)

	reader = iobuf.AsyncReadCloser(func() (*http.Response, error) {
		now := time.Now()
		c.log.Debugf("doProxy[tail]: begin: %s, rawRange: %s, newRange: %s", now, rawRange, newRange)
		resp, err1 := c.doProxy(req, true)
		c.log.Debugf("doProxy[tail]: timeCost: %s, rawRange: %s, newRange: %s", time.Since(now), rawRange, newRange)

		if err1 != nil {
			return nil, err
		}
		return resp, err1
	})

	return reader, len(reqChunks) - int(from), nil
}

func getChunkSlice(c *Caching, from uint32) (*os.File, error) {
	f, err := ropen(c.id.WPathSlice(c.bucket.Path(), from))
	if err == nil {
		return f, nil
	}

	if err != nil && !os.IsNotExist(err) {
		if isTooManyFiles(err) {
			return nil, err
		}
		c.log.Errorf("unexpected error while trying to load %s from storage: %s", from, err)
		return nil, err
	}
	return nil, os.ErrNotExist
}

func checkChunkSize(c *Caching, f *os.File, idx uint32) error {
	stat, err := f.Stat()
	if err != nil {
		c.log.Errorf("failed to stat chunk file %s: %s", f.Name(), err)
		return err
	}

	size := stat.Size()
	realSize := c.opt.SliceSize
	if c.md.BlockSize > 0 {
		realSize = c.md.BlockSize
	}

	if idx == uint32(c.md.Size/realSize) {
		realSize = c.md.Size % realSize // last chunk size
	}

	if size != int64(realSize) {
		c.log.Errorf("chunk file %s size mismatch: expected %d, got %d", f.Name(), realSize, size)
		return fmt.Errorf("content size(%d) != real size(%d); fileName: %s", size, realSize, f.Name())
	}

	return nil
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
