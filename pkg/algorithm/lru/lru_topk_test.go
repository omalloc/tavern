package lru

import (
	"testing"
)

func TestCache_TopK(t *testing.T) {
	c := New[string, int](10)

	// Add items
	c.Set("a", 1) // freq 1
	c.Set("b", 1) // freq 1
	c.Set("c", 1) // freq 1
	c.Set("d", 1) // freq 1

	// Increase frequency
	c.Get("a") // a: freq 2
	c.Get("a") // a: freq 3

	c.Get("b") // b: freq 2

	// Current state:
	// a: 3
	// b: 2
	// c: 1
	// d: 1

	// Test TopK(2) should return [a, b]
	top2 := c.TopK(2)
	if len(top2) != 2 {
		t.Fatalf("expected len 2, got %d", len(top2))
	}
	if top2[0] != "a" {
		t.Errorf("expected top[0] to be 'a', got %s", top2[0])
	}
	if top2[1] != "b" {
		t.Errorf("expected top[1] to be 'b', got %s", top2[1])
	}

	// Increase c to be top
	c.Get("c")
	c.Get("c")
	c.Get("c")
	// c: 4 -> New Top 1

	// Test TopK(3)
	top3 := c.TopK(3)
	if len(top3) != 3 {
		t.Fatalf("expected len 3, got %d", len(top3))
	}
	if top3[0] != "c" { // c(4)
		t.Errorf("expected top[0] to be 'c', got %s", top3[0])
	}
	if top3[1] != "a" { // a(3)
		t.Errorf("expected top[1] to be 'a', got %s", top3[1])
	}
	if top3[2] != "b" { // b(2)
		t.Errorf("expected top[2] to be 'b', got %s", top3[2])
	}

	// Test TopK > Len
	top10 := c.TopK(10)
	if len(top10) != 4 {
		t.Errorf("expected len 4, got %d", len(top10))
	}
}
