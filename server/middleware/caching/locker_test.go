package caching

import (
	"fmt"
	"sync"
	"testing"
)

func TestResourceLockerBoundsDistinctURLLocks(t *testing.T) {
	locker := globalLocker
	first := locker.getLock("http://example.com/first")
	seen := make(map[*sync.RWMutex]struct{})
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("http://example.com/object/%d", i)
		lock := locker.getLock(key)
		lock.Lock()
		lock.Unlock()
		seen[lock] = struct{}{}
	}
	if len(seen) > 256 {
		t.Fatalf("10000 URLs retained %d locks, want at most 256", len(seen))
	}
	if locker.getLock("http://example.com/first") != first {
		t.Fatal("lock identity changed after URL churn")
	}
}
