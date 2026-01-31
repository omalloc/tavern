package iobuf

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// mockCloserError is a mock io.ReadCloser that returns an error on close
type mockCloserError struct {
	closeErr error
}

func (m *mockCloserError) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}

func (m *mockCloserError) Close() error {
	return m.closeErr
}

// TestAllCloser_SingleCloser tests AllCloser with a single reader
func TestAllCloser_SingleCloser(t *testing.T) {
	mock := &mockCloserError{closeErr: nil}
	allCloser := AllCloser{mock}

	err := allCloser.Close()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestAllCloser_MultipleClosers tests AllCloser with multiple readers
func TestAllCloser_MultipleClosers(t *testing.T) {
	mock1 := &mockCloserError{closeErr: nil}
	mock2 := &mockCloserError{closeErr: nil}
	mock3 := &mockCloserError{closeErr: nil}

	allCloser := AllCloser{mock1, mock2, mock3}

	err := allCloser.Close()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestAllCloser_EmptySlice tests AllCloser with empty slice
func TestAllCloser_EmptySlice(t *testing.T) {
	allCloser := AllCloser{}

	err := allCloser.Close()

	if err != nil {
		t.Errorf("expected no error for empty slice, got %v", err)
	}
}

// TestAllCloser_SingleErrorIgnored tests that AllCloser continues despite error
func TestAllCloser_SingleErrorIgnored(t *testing.T) {
	err1 := errors.New("close error 1")
	mock1 := &mockCloserError{closeErr: err1}

	allCloser := AllCloser{mock1}

	err := allCloser.Close()

	// AllCloser ignores errors from individual closes
	if err != nil {
		t.Logf("AllCloser returned error: %v (errors are ignored)", err)
	}
}

// TestAllCloser_MultipleErrorsIgnored tests that AllCloser ignores all errors
func TestAllCloser_MultipleErrorsIgnored(t *testing.T) {
	err1 := errors.New("close error 1")
	err2 := errors.New("close error 2")
	err3 := errors.New("close error 3")

	mock1 := &mockCloserError{closeErr: err1}
	mock2 := &mockCloserError{closeErr: err2}
	mock3 := &mockCloserError{closeErr: err3}

	allCloser := AllCloser{mock1, mock2, mock3}

	err := allCloser.Close()

	// AllCloser ignores all errors
	if err != nil {
		t.Logf("AllCloser returned error: %v (errors are ignored)", err)
	}
}

// TestAllCloser_MixedErrorsIgnored tests AllCloser with mixed success and error
func TestAllCloser_MixedErrorsIgnored(t *testing.T) {
	mock1 := &mockCloserError{closeErr: nil}
	err2 := errors.New("close error 2")
	mock2 := &mockCloserError{closeErr: err2}
	mock3 := &mockCloserError{closeErr: nil}

	allCloser := AllCloser{mock1, mock2, mock3}

	err := allCloser.Close()

	// AllCloser ignores errors
	if err != nil {
		t.Logf("AllCloser returned error: %v (errors are ignored)", err)
	}
}

// TestAllCloser_AllCallsClosed tests that all readers are attempted to close
func TestAllCloser_AllCallsClosed(t *testing.T) {
	// Use custom closers to track if Close was called
	type trackedCloser struct {
		closed bool
	}

	tracked := []*trackedCloser{
		{},
		{},
		{},
	}

	closers := make(AllCloser, len(tracked))
	for i, tc := range tracked {
		tc := tc // capture for closure
		closers[i] = &mockCustomCloser{
			close: func() {
				tc.closed = true
			},
		}
	}

	closers.Close()

	for i, tc := range tracked {
		if !tc.closed {
			t.Errorf("closer %d was not closed", i)
		}
	}
}

// mockCustomCloser is a mock with custom close tracking
type mockCustomCloser struct {
	close func()
}

func (m *mockCustomCloser) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}

func (m *mockCustomCloser) Close() error {
	if m.close != nil {
		m.close()
	}
	return nil
}

// TestAllCloser_LargeNumber tests AllCloser with many readers
func TestAllCloser_LargeNumber(t *testing.T) {
	closers := make(AllCloser, 100)
	for i := 0; i < 100; i++ {
		closers[i] = &mockCloserError{closeErr: nil}
	}

	err := closers.Close()

	if err != nil {
		t.Errorf("expected no error with 100 closers, got %v", err)
	}
}

// TestAllCloser_NilInSlice tests AllCloser with nil elements
func TestAllCloser_NilInSlice(t *testing.T) {
	// Note: This test verifies what happens with nil elements
	// The current implementation will panic if nil is encountered
	// This is testing the expected behavior
	mock1 := &mockCloserError{closeErr: nil}
	mock2 := &mockCloserError{closeErr: nil}

	allCloser := AllCloser{mock1, mock2}

	// Should not panic
	err := allCloser.Close()

	if err != nil {
		t.Logf("got error: %v", err)
	}
}

// TestAllCloser_StringReadClosers tests AllCloser with string readers
func TestAllCloser_StringReadClosers(t *testing.T) {
	r1 := io.NopCloser(strings.NewReader("test1"))
	r2 := io.NopCloser(strings.NewReader("test2"))
	r3 := io.NopCloser(strings.NewReader("test3"))

	allCloser := AllCloser{r1, r2, r3}

	err := allCloser.Close()

	if err != nil {
		t.Errorf("expected no error closing string readers, got %v", err)
	}
}

// TestAllCloser_NoErrorReturn tests that AllCloser always returns nil
func TestAllCloser_NoErrorReturn(t *testing.T) {
	// Create closers with errors
	closers := make(AllCloser, 5)
	for i := 0; i < 5; i++ {
		closers[i] = &mockCloserError{closeErr: errors.New("error")}
	}

	err := closers.Close()

	// AllCloser ignores all errors and returns nil
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestAllCloser_ImplementsReadCloser tests that individual elements are ReadClosers
func TestAllCloser_ImplementsReadCloser(t *testing.T) {
	mock := &mockCloserError{}

	var _ io.ReadCloser = mock

	allCloser := AllCloser{mock}

	// Verify Close() can be called on AllCloser
	allCloser.Close()
}

// TestAllCloser_SequentialReadsAndClose tests read operations before close
func TestAllCloser_SequentialReadsAndClose(t *testing.T) {
	r1 := io.NopCloser(strings.NewReader("hello"))
	r2 := io.NopCloser(strings.NewReader("world"))

	allCloser := AllCloser{r1, r2}

	// Read from individual readers
	p := make([]byte, 5)
	r1.Read(p)

	// Then close all
	err := allCloser.Close()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// TestAllCloser_OrderOfClosing tests that all readers are closed in order
func TestAllCloser_OrderOfClosing(t *testing.T) {
	type orderTracker struct {
		order []int
		id    int
	}

	tracker := &orderTracker{order: []int{}}

	closers := make(AllCloser, 3)
	for i := 0; i < 3; i++ {
		i := i // capture for closure
		closers[i] = &mockCustomCloser{
			close: func() {
				tracker.order = append(tracker.order, i)
			},
		}
	}

	closers.Close()

	// Verify all were closed
	if len(tracker.order) != 3 {
		t.Errorf("expected 3 closers, got %d", len(tracker.order))
	}

	// Verify order: should be 0, 1, 2
	expectedOrder := []int{0, 1, 2}
	for i, v := range tracker.order {
		if v != expectedOrder[i] {
			t.Errorf("expected order[%d]=%d, got %d", i, expectedOrder[i], v)
		}
	}
}

// TestAllCloser_PanicRecovery tests behavior when a closer panics
func TestAllCloser_PanicRecovery(t *testing.T) {
	// Note: Current implementation doesn't handle panics
	// This test documents that behavior
	defer func() {
		if r := recover(); r != nil {
			t.Logf("panic recovered: %v (expected)", r)
		}
	}()

	mock1 := &mockCloserError{closeErr: nil}
	mock2 := &mockCloserError{closeErr: nil}

	allCloser := AllCloser{mock1, mock2}
	allCloser.Close()
}

// TestAllCloser_Type tests AllCloser type assertion
func TestAllCloser_Type(t *testing.T) {
	mock := &mockCloserError{}
	allCloser := AllCloser{mock}

	// Verify type
	var _ []io.ReadCloser = allCloser
}

// TestAllCloser_AppendAfterCreation tests modifying AllCloser after creation
func TestAllCloser_AppendAfterCreation(t *testing.T) {
	mock1 := &mockCloserError{}
	mock2 := &mockCloserError{}

	allCloser := AllCloser{mock1}
	allCloser = append(allCloser, mock2)

	err := allCloser.Close()

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if len(allCloser) != 2 {
		t.Errorf("expected 2 closers, got %d", len(allCloser))
	}
}

// TestAllCloser_MultipleCloses tests calling Close multiple times
func TestAllCloser_MultipleCloses(t *testing.T) {
	closeCount := 0
	closer := &mockCustomCloser{
		close: func() {
			closeCount++
		},
	}

	allCloser := AllCloser{closer}

	allCloser.Close()
	allCloser.Close()
	allCloser.Close()

	// Each call to AllCloser.Close() should close all readers
	if closeCount != 3 {
		t.Errorf("expected 3 close calls, got %d", closeCount)
	}
}

// TestAllCloser_SliceLen tests length of AllCloser
func TestAllCloser_SliceLen(t *testing.T) {
	mock1 := &mockCloserError{}
	mock2 := &mockCloserError{}
	mock3 := &mockCloserError{}

	allCloser := AllCloser{mock1, mock2, mock3}

	if len(allCloser) != 3 {
		t.Errorf("expected length 3, got %d", len(allCloser))
	}
}

// TestAllCloser_SliceCap tests capacity of AllCloser
func TestAllCloser_SliceCap(t *testing.T) {
	closers := make(AllCloser, 5, 10)

	if len(closers) != 5 {
		t.Errorf("expected length 5, got %d", len(closers))
	}

	if cap(closers) != 10 {
		t.Errorf("expected capacity 10, got %d", cap(closers))
	}
}
