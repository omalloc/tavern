package caching

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	storagev1 "github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/contrib/log"
	"github.com/omalloc/tavern/proxy"
	"github.com/omalloc/tavern/storage"
)

// Processor defines the interface for caching processor middleware.
type Processor interface {
	// Lookup checks if the request hits the cache.
	Lookup(caching *Caching, req *http.Request) (bool, error)
	// PreRequest processes the request before sending it to the origin server.
	PreRequest(caching *Caching, req *http.Request) (*http.Request, error)
	// PostRequest processes the response received from the origin server.
	PostRequest(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error)
}

// ProcessorChain represents a chain of caching processors.
type ProcessorChain []Processor

// Lookup iterates through the processor chain to check for a cache hit.
func (pc *ProcessorChain) Lookup(caching *Caching, req *http.Request) (bool, error) {
	var err error
	for _, processor := range *pc {
		caching.hit, err = processor.Lookup(caching, req)
		if err != nil {
			return false, err
		}

		if !caching.hit {
			// TIPS: PRINT DEBUG CODE
			if caching.log.Enabled(log.LevelDebug) {
				typeof := reflect.TypeOf(processor).Elem()
				caching.log.Debugf("%s.Lookup() result %t", typeof.Name(), caching.hit)
			}
			return false, nil
		}
	}
	return true, nil
}

// PreRequest processes the request through the processor chain before sending it to the origin server.
func (pc *ProcessorChain) PreRequest(caching *Caching, req *http.Request) (*http.Request, error) {
	var err error
	for _, processor := range *pc {
		req, err = processor.PreRequest(caching, req)
		if err != nil {
			if caching.log.Enabled(log.LevelDebug) {
				typeof := reflect.TypeOf(processor).Elem()
				caching.log.Warnf("%s.Lookup() result %t", typeof.Name(), caching.hit)
			}
			return req, err
		}
	}
	return req, nil
}

// PostRequest processes the response through the processor chain after receiving it from the origin server.
func (pc *ProcessorChain) PostRequest(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	var err error
	for _, processor := range *pc {
		resp, err = processor.PostRequest(caching, req, resp)
		if err != nil {
			if caching.log.Enabled(log.LevelDebug) {
				typeof := reflect.TypeOf(processor).Elem()
				caching.log.Warnf("%s.PostRequst() error: %v", typeof.Name(), err)
			}
			return resp, err
		}
	}
	return resp, nil
}

func (pc *ProcessorChain) preCacheProcessor(proxyClient proxy.Proxy, opt *cachingOption, req *http.Request) (*Caching, error) {
	objectID, err := newObjectIDFromRequest(req, "", opt.IncludeQueryInCacheKey)
	if err != nil {
		return nil, fmt.Errorf("failed new object-objectID from request err: %w", err)
	}

	// Select storage bucket by object ID
	// hashring or diskhash
	bucket := storage.Select(req.Context(), objectID)
	// lookup cache with cache-key
	md, _ := bucket.Lookup(req.Context(), objectID)

	// TODO: object pool.
	//caching := cachingPool.Get().(*Caching)
	//caching.log = log.Context(req.Context())
	//caching.proxyClient = proxyClient
	//caching.opt = opt
	//caching.id = objectID
	//caching.bucket = bucket
	//caching.req = req
	//caching.md = md
	//caching.processor = pc
	//caching.cacheStatus = storagev1.CacheMiss

	caching := &Caching{
		log:         log.Context(req.Context()),
		proxyClient: proxyClient,
		opt:         opt,
		id:          objectID,
		bucket:      bucket,
		req:         req,
		md:          md,
		processor:   pc,
		cacheStatus: storagev1.CacheMiss,
	}

	hit, err := pc.Lookup(caching, req)
	if err != nil {
		caching.log.Errorf("failed lookup cache err: %v", err)
	}
	caching.hit = hit

	return caching, nil
}

func (pc *ProcessorChain) postCacheProcessor(caching *Caching, _ *http.Request, resp *http.Response) (*http.Response, error) {
	caching.setXCache(resp)

	if resp != nil && resp.Header != nil && caching.md != nil {
		resp.Header.Set("Age", strconv.FormatInt(time.Now().Unix()-caching.md.RespUnix, 10))
		resp.Header.Set("Date", time.Unix(caching.md.RespUnix, 0).Local().UTC().Format(http.TimeFormat))
		resp.Header.Set("Expires", time.Unix(caching.md.ExpiresAt, 0).Local().UTC().Format(http.TimeFormat))
	}

	// FETCH request
	if resp != nil && caching.prefetch {
		_, _ = io.Copy(io.Discard, resp.Body)
	}

	// TODO: incr index ref count.

	return resp, nil
}

// String returns a string representation of the processor chain.
func (pc *ProcessorChain) String() string {
	sb := strings.Builder{}
	for i, processor := range *pc {
		if i > 0 {
			sb.WriteString(" -> ")
		}
		typeof := reflect.TypeOf(processor).Elem()
		sb.WriteString(typeof.Name())
	}
	return sb.String()
}

// NewProcessorChain creates a new ProcessorChain with the given processors.
func NewProcessorChain(processors ...Processor) *ProcessorChain {
	pc := ProcessorChain(processors)
	return &pc
}

// fill removes any nil processors from the chain.
func (pc *ProcessorChain) fill() *ProcessorChain {
	*pc = slices.DeleteFunc(*pc, func(p Processor) bool {
		return p == nil
	})
	return pc
}
