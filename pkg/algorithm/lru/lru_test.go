package lru

import (
	"sync"
	"testing"
)

func TestCache_Basic(t *testing.T) {
	c := New[string, int](2)
	c.Set("a", 1)
	c.Set("b", 2)

	if *c.Get("a") != 1 {
		t.Errorf("expected 1, got %v", *c.Get("a"))
	}
	if *c.Get("b") != 2 {
		t.Errorf("expected 2, got %v", *c.Get("b"))
	}

	c.Set("c", 3) // Should evict something. Since it's LFU, and "a", "b" both have freq 1 (after Get), we'll see.
	// Actually "a" and "b" have freq 2 now.
	if c.Len() != 2 {
		t.Errorf("expected len 2, got %d", c.Len())
	}
}

func TestCache_Race(t *testing.T) {
	c := New[string, int](100)
	var wg sync.WaitGroup

	// Concurrent Sets
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Set(string(rune(i)), i)
		}(i)
	}

	// Concurrent Keys() - this should trigger the race detector if not fixed
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Keys()
		}()
	}

	// Concurrent Gets
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Get(string(rune(i)))
		}(i)
	}

	wg.Wait()
}

func TestCache_EvictionChannel_NonBlocking(t *testing.T) {
	evictCh := make(chan Eviction[string, int]) // Unbuffered channel
	c := New[string, int](1)
	c.EvictionChannel = evictCh

	c.Set("a", 1)
	// This Set should trigger eviction of "a". 
	// Since evictCh is unbuffered and we are not reading from it, 
	// it would block if not for our fix.
	c.Set("b", 2) 

	if c.Len() != 1 {
		t.Errorf("expected len 1, got %d", c.Len())
	}
	if c.Has("a") {
		t.Error("expected 'a' to be evicted")
	}
}

func TestCache_Remove(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	if !c.Remove("a") {
		t.Error("expected true")
	}
	if c.Has("a") {
		t.Error("expected false")
	}
	if c.Len() != 0 {
		t.Errorf("expected len 0, got %d", c.Len())
	}
}

func TestCache_Purge(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Purge()
	if c.Len() != 0 {
		t.Errorf("expected len 0, got %d", c.Len())
	}
	if c.Has("a") || c.Has("b") {
		t.Error("expected cache to be empty")
	}
}

func TestCache_Peek(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)

	// Peek should return the value
	val := c.Peek("a")
	if val == nil || *val != 1 {
		t.Errorf("expected 1, got %v", val)
	}

	// Peek should not increment frequency
	freq := c.GetFrequency("a")
	if freq != 1 {
		t.Errorf("expected frequency 1, got %d", freq)
	}

	// Peek non-existent key
	if c.Peek("nonexistent") != nil {
		t.Error("expected nil for non-existent key")
	}
}

func TestCache_GetFrequency(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)

	// Initial frequency is 1
	if freq := c.GetFrequency("a"); freq != 1 {
		t.Errorf("expected frequency 1, got %d", freq)
	}

	// Get increments frequency
	c.Get("a")
	if freq := c.GetFrequency("a"); freq != 2 {
		t.Errorf("expected frequency 2, got %d", freq)
	}

	// Non-existent key returns 0
	if freq := c.GetFrequency("nonexistent"); freq != 0 {
		t.Errorf("expected frequency 0, got %d", freq)
	}
}

func TestCache_Get_NonExistent(t *testing.T) {
	c := New[string, int](10)
	if c.Get("nonexistent") != nil {
		t.Error("expected nil for non-existent key")
	}
}

func TestCache_Remove_NonExistent(t *testing.T) {
	c := New[string, int](10)
	if c.Remove("nonexistent") {
		t.Error("expected false for non-existent key")
	}
}

func TestCache_Evict(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	// Evict 2 items
	evicted := c.Evict(2)
	if evicted != 2 {
		t.Errorf("expected 2 evicted, got %d", evicted)
	}
	if c.Len() != 1 {
		t.Errorf("expected len 1, got %d", c.Len())
	}
}

func TestCache_Evict_MoreThanExists(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)

	// Try to evict more than exists
	evicted := c.Evict(10)
	if evicted != 1 {
		t.Errorf("expected 1 evicted, got %d", evicted)
	}
	if c.Len() != 0 {
		t.Errorf("expected len 0, got %d", c.Len())
	}
}

func TestCache_Set_UpdateExisting(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("a", 2)

	val := c.Get("a")
	if val == nil || *val != 2 {
		t.Errorf("expected 2, got %v", val)
	}

	// Frequency should be 3 (set, set, get)
	freq := c.GetFrequency("a")
	if freq != 3 {
		t.Errorf("expected frequency 3, got %d", freq)
	}
}

func TestCache_FrequencyBucketReuse(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)

	// Both have freq 1
	c.Get("a") // a has freq 2
	c.Get("a") // a has freq 3
	c.Get("b") // b has freq 2

	if c.GetFrequency("a") != 3 {
		t.Errorf("expected frequency 3 for a, got %d", c.GetFrequency("a"))
	}
	if c.GetFrequency("b") != 2 {
		t.Errorf("expected frequency 2 for b, got %d", c.GetFrequency("b"))
	}
}

func TestCache_EvictionChannel(t *testing.T) {
	evictCh := make(chan Eviction[string, int], 10)
	c := New[string, int](2)
	c.EvictionChannel = evictCh

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3) // Should evict "a" or "b" (both have freq 1, "a" is older)

	select {
	case ev := <-evictCh:
		if ev.Key != "a" {
			t.Errorf("expected 'a' to be evicted first, got %s", ev.Key)
		}
	default:
		t.Error("expected eviction notification")
	}
}

func TestCache_NoBounds(t *testing.T) {
	c := New[string, int](0) // No bounds
	for i := 0; i < 100; i++ {
		c.Set(string(rune('a'+i)), i)
	}
	if c.Len() != 100 {
		t.Errorf("expected len 100, got %d", c.Len())
	}
}
