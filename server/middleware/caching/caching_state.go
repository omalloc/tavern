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

// PreRequest implements Processor.
func (s *StateProcessor) PreRequest(caching *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

// PostRequest implements Processor.
func (s *StateProcessor) PostRequest(caching *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	return resp, nil
}

func NewStateProcessor() Processor {
	return &StateProcessor{}
}
