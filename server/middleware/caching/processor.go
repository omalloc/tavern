package caching

import (
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/omalloc/tavern/contrib/log"
)

// Processor defines the interface for caching processor middleware.
type Processor interface {
	// Lookup checks if the request hits the cache.
	Lookup(caching *Caching, req *http.Request) (bool, error)
	// PreRequst processes the request before sending it to the origin server.
	PreRequst(caching *Caching, req *http.Request) (*http.Request, error)
	// PostRequst processes the response received from the origin server.
	PostRequst(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error)
}

// ProcessorChain represents a chain of caching processors.
type ProcessorChain []Processor

// Lookup iterates through the processor chain to check for a cache hit.
func (pc *ProcessorChain) Lookup(caching *Caching, req *http.Request) (bool, error) {
	var err error
	for _, processor := range *pc {
		caching.hit, err = processor.Lookup(caching, req)
		if err != nil {
			if caching.log.Enabled(log.LevelDebug) {
				typeof := reflect.TypeOf(processor).Elem()
				caching.log.Debugf("%s.Lookup() result %t", typeof.Name(), caching.hit)
			}
			return false, err
		}
	}
	return true, nil
}

// PreRequst processes the request through the processor chain before sending it to the origin server.
func (pc *ProcessorChain) PreRequst(caching *Caching, req *http.Request) (*http.Request, error) {
	var err error
	for _, processor := range *pc {
		req, err = processor.PreRequst(caching, req)
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

// PostRequst processes the response through the processor chain after receiving it from the origin server.
func (pc *ProcessorChain) PostRequst(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	var err error
	for _, processor := range *pc {
		resp, err = processor.PostRequst(caching, req, resp)
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

func (pc *ProcessorChain) preCacheProcessor(req *http.Request) (*Caching, error) {
	caching := &Caching{
		req:       req,
		processor: pc,
	}
	return caching, nil
}

func (pc *ProcessorChain) postCacheProcessor(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	// TODO: add response processing logic
	// TODO: add X-Cache headers
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
