package heavykeeper

import (
	"fmt"
	"testing"
)

func TestHeavyKeeper_AddAndQuery(t *testing.T) {
	hk := New(3, 1024, 0.9)

	key := []byte("test-key")

	for i := 0; i < 100; i++ {
		hk.Add(key)
	}

	count := hk.Query(key)
	if count < 80 { // Allow some error due to decay/collision
		t.Errorf("Expected count around 100, got %d", count)
	}

	// Test another key
	key2 := []byte("another-key")
	count2 := hk.Query(key2)
	if count2 != 0 {
		t.Errorf("Expected count 0 for new key, got %d", count2)
	}
}

func TestHeavyKeeper_Decay(t *testing.T) {
	hk := New(3, 10, 0.9) // Small width to force collisions

	// Fill with "noise"
	for i := 0; i < 1000; i++ {
		key := []byte(fmt.Sprintf("noise-%d", i))
		hk.Add(key)
	}

	// Add "heavy" item
	heavyKey := []byte("heavy")
	for i := 0; i < 100; i++ {
		hk.Add(heavyKey)
	}

	count := hk.Query(heavyKey)
	t.Logf("Heavy key count: %d", count)
	if count < 50 {
		t.Errorf("Heavy key count too low: %d", count)
	}
}

func TestHeavyKeeper_Clear(t *testing.T) {
	hk := New(3, 1024, 0.9)
	key := []byte("test")
	hk.Add(key)
	if hk.Query(key) == 0 {
		t.Fatal("Should verify")
	}

	hk.Clear()
	if hk.Query(key) != 0 {
		t.Fatal("Should be 0 after Clear")
	}
}

func BenchmarkHeavyKeeper_Add(b *testing.B) {
	hk := New(3, 4096, 0.9)
	key := []byte("bench-key")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hk.Add(key)
	}
}

func BenchmarkHeavyKeeper_AddRandom(b *testing.B) {
	hk := New(3, 4096, 0.9)
	keys := make([][]byte, 1000)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("key-%d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hk.Add(keys[i%1000])
	}
}
