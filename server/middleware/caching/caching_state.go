package caching

import "net/http"

type StateProcessor struct{}

// Lookup implements Processor.
func (s *StateProcessor) Lookup(caching *Caching, req *http.Request) (bool, error) {
	// index metadata nil
	if caching.md == nil {
		return false, nil
	}

	return true, nil
}

// PostRequst implements Processor.
func (s *StateProcessor) PostRequst(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	panic("unimplemented")
}

// PreRequst implements Processor.
func (s *StateProcessor) PreRequst(caching *Caching, req *http.Request) (*http.Request, error) {
	panic("unimplemented")
}

func NewStateProcessor() Processor {
	return &StateProcessor{}
}
