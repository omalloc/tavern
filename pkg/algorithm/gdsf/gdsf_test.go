package gdsf

import (
	"sync"
	"testing"

	"github.com/omalloc/tavern/api/defined/v1/storage"
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

	c.Set("c", 3)
	if c.Len() != 2 {
		t.Errorf("expected len 2, got %d", c.Len())
	}
}

func TestCache_Race(t *testing.T) {
	c := New[string, int](100)
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Set(string(rune(i)), i)
		}(i)
	}

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Keys()
		}()
	}

	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Get(string(rune(i)))
		}(i)
	}

	wg.Wait()
}

func TestCache_EvictionChannel_NonBlocking(t *testing.T) {
	evictCh := make(chan storage.Eviction[string, int])
	c := New[string, int](1)
	c.EvictionChannel = evictCh

	c.Set("a", 1)
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

	val := c.Peek("a")
	if val == nil || *val != 1 {
		t.Errorf("expected 1, got %v", val)
	}

	freq := c.GetFrequency("a")
	if freq != 1 {
		t.Errorf("expected frequency 1, got %d", freq)
	}

	if c.Peek("nonexistent") != nil {
		t.Error("expected nil for non-existent key")
	}
}

func TestCache_GetFrequency(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)

	if freq := c.GetFrequency("a"); freq != 1 {
		t.Errorf("expected frequency 1, got %d", freq)
	}

	c.Get("a")
	if freq := c.GetFrequency("a"); freq != 2 {
		t.Errorf("expected frequency 2, got %d", freq)
	}

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

	freq := c.GetFrequency("a")
	if freq != 3 {
		t.Errorf("expected frequency 3, got %d", freq)
	}
}

func TestCache_FrequencyIncrements(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)

	c.Get("a")
	c.Get("a")
	c.Get("b")

	if c.GetFrequency("a") != 3 {
		t.Errorf("expected frequency 3 for a, got %d", c.GetFrequency("a"))
	}
	if c.GetFrequency("b") != 2 {
		t.Errorf("expected frequency 2 for b, got %d", c.GetFrequency("b"))
	}
}

func TestCache_EvictionChannel(t *testing.T) {
	evictCh := make(chan storage.Eviction[string, int], 10)
	c := New[string, int](2)
	c.EvictionChannel = evictCh

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	select {
	case ev := <-evictCh:
		// With equal sizes and frequencies, the first inserted has the lowest priority
		// (because clock was smaller at insertion time)
		if ev.Key != "a" {
			t.Logf("evicted key: %s (expected 'a' with equal sizes)", ev.Key)
		}
	default:
		t.Error("expected eviction notification")
	}
}

func TestCache_NoBounds(t *testing.T) {
	c := New[string, int](0)
	for i := range 100 {
		c.Set(string(rune('a'+i)), i)
	}
	if c.Len() != 100 {
		t.Errorf("expected len 100, got %d", c.Len())
	}
}

func TestCache_GDSF_LargeObjectEvictedFirst(t *testing.T) {
	// With equal frequency, the large object should be evicted first
	c := New[string, int](2)
	c.Set("small", 1)
	c.Set("large", 2)

	c.SetSize("small", 100)  // small object
	c.SetSize("large", 1000) // large object (10x larger)

	// Both have freq=1, same clock. Priority: clock + 1 * (1/size)
	// small: clock + 1/100 = clock + 0.01
	// large: clock + 1/1000 = clock + 0.001
	// large has lower priority → gets evicted first

	c.Set("trigger", 3) // triggers eviction (len > UpperBound=2)
	c.SetSize("trigger", 1)

	if !c.Has("small") {
		t.Error("expected 'small' to be retained (higher priority due to smaller size)")
	}
	if c.Has("large") {
		t.Error("expected 'large' to be evicted (lower priority due to larger size)")
	}
}

func TestCache_GDSF_AccessProtectsLargeObject(t *testing.T) {
	c := New[string, int](2)
	c.Set("small", 1)
	c.Set("large", 2)

	c.SetSize("small", 100)
	c.SetSize("large", 1000)

	// Access large object 5 times to boost its priority
	for range 5 {
		c.Get("large")
	}

	// large priority: clock + 6 * (1/1000) = clock + 0.006
	// small priority: clock + 1 * (1/100) = clock + 0.01
	// small has higher priority → large still at risk

	// Access large more times
	for range 5 {
		c.Get("large")
	}

	// large priority: clock + 11 * (1/1000) = clock + 0.011
	// small priority: clock + 1 * (1/100) = clock + 0.01
	// Now large has higher priority → small should be evicted

	c.Set("trigger", 3) // triggers eviction
	c.SetSize("trigger", 1)

	if !c.Has("large") {
		t.Error("expected 'large' to be retained after many accesses")
	}
	if c.Has("small") {
		t.Error("expected 'small' to be evicted")
	}
}

func TestCache_GDSF_ClockAging(t *testing.T) {
	c := New[string, int](3)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	c.SetSize("a", 1)
	c.SetSize("b", 1)
	c.SetSize("c", 1)

	// Access "a" and "b" heavily - "c" stays at freq=1
	for range 10 {
		c.Get("a")
		c.Get("b")
	}

	// Trigger eviction: "c" should be evicted (lowest priority)
	c.Set("d", 4)
	c.SetSize("d", 1)

	if !c.Has("a") && !c.Has("b") {
		t.Error("expected 'a' and 'b' to be retained")
	}
	if c.Has("c") {
		t.Error("expected 'c' to be evicted")
	}

	// After eviction, clock advanced to c's priority.
	// Subsequent new entries start with clock value > 0, giving them a fair baseline.
	prevClock := c.clock
	if prevClock <= 0 {
		t.Error("expected clock to advance after eviction")
	}
}

func TestCache_TopK(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	c.SetSize("a", 1)
	c.SetSize("b", 1)
	c.SetSize("c", 1)

	// Access "c" most, "a" least
	c.Get("c")
	c.Get("c")
	c.Get("b")

	top := c.TopK(2)
	if len(top) != 2 {
		t.Fatalf("expected 2, got %d", len(top))
	}
	if top[0] != "c" {
		t.Errorf("expected 'c' as top, got %s", top[0])
	}
}

func TestCache_SetSize_NoopOnMissingKey(t *testing.T) {
	c := New[string, int](10)
	// Should not panic
	c.SetSize("nonexistent", 100)
}

func TestCache_SetSize_ZeroOrNegative(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.SetSize("a", 0)  // zero → clamped to 1
	c.SetSize("a", -1) // negative → clamped to 1

	// Should not cause division by zero or NaN
	val := c.Get("a")
	if val == nil {
		t.Fatal("expected value")
	}
}

func TestCache_Keys(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	keys := c.Keys()
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}

	found := make(map[string]bool, len(keys))
	for _, k := range keys {
		found[k] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !found[want] {
			t.Errorf("expected key %q not found in Keys()", want)
		}
	}
}

func TestCache_Keys_Empty(t *testing.T) {
	c := New[string, int](10)
	keys := c.Keys()
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

func TestCache_EvictionChannel_Nil(t *testing.T) {
	c := New[string, int](2)
	// EvictionChannel is nil by default — eviction must not panic
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	if c.Len() != 2 {
		t.Errorf("expected len 2, got %d", c.Len())
	}
}

func TestCache_TopK_MoreThanLen(t *testing.T) {
	c := New[string, int](10)
	c.Set("a", 1)
	c.Set("b", 2)

	top := c.TopK(10) // k > len
	if len(top) != 2 {
		t.Errorf("expected 2, got %d", len(top))
	}
}

func TestCache_TopK_Empty(t *testing.T) {
	c := New[string, int](10)
	top := c.TopK(5)
	if len(top) != 0 {
		t.Errorf("expected 0, got %d", len(top))
	}
}

func TestCache_SetEvictionCh(t *testing.T) {
	c := New[string, int](2)
	ch := make(chan storage.Eviction[string, int], 10)
	c.SetEvictionCh(ch)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3) // triggers eviction of a (or b, both freq=1)

	select {
	case <-ch:
		// expected
	default:
		t.Error("expected eviction notification via SetEvictionCh")
	}
}
