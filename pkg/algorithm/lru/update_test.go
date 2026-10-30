package lru

import (
	"sync"
	"testing"
)

func TestUpdateOwnsConcurrentMutations(t *testing.T) {
	c := New[string, int](1)
	c.Set("key", 0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				c.Update("key", func(v int) int { return v + 1 })
				if v := c.Get("key"); v != nil {
					*v = -1
				}
				if v := c.Peek("key"); v != nil {
					*v = -2
				}
			}
		})
	}
	wg.Wait()
	if got := c.Peek("key"); got == nil || *got != 8000 {
		t.Fatal(got)
	}
	if c.Update("missing", func(v int) int { t.Fatal("updated missing entry"); return v }) {
		t.Fatal("created missing entry")
	}
}
