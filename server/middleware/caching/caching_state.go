package caching

import "net/http"

type StateProcessor struct{}

// Lookup implements Processor.
func (s *StateProcessor) Lookup(c *Caching, req *http.Request) (bool, error) {
	// index metadata nil
	if c.md == nil {
		return false, nil
	}
	return true, nil
}

// PreRequest implements Processor.
func (s *StateProcessor) PreRequest(c *Caching, req *http.Request) (*http.Request, error) {
	return req, nil
}

// PostRequest implements Processor.
func (s *StateProcessor) PostRequest(c *Caching, req *http.Request, resp *http.Response) (*http.Response, error) {
	return resp, nil
}

func NewStateProcessor() Processor {
	return &StateProcessor{}
}
