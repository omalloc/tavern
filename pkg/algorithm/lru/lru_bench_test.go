package lru

import (
	"fmt"
	"math/rand"
	"testing"
)

func BenchmarkCache_Set(b *testing.B) {
	c := New[string, int](b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}
}

func BenchmarkCache_Get(b *testing.B) {
	c := New[string, int](b.N)
	for i := 0; i < b.N; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(fmt.Sprintf("key-%d", i))
	}
}

func BenchmarkCache_Eviction(b *testing.B) {
	capacity := 1000
	c := New[string, int](capacity)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}
}

func BenchmarkCache_Concurrent_Mixed(b *testing.B) {
	capacity := 10000
	c := New[string, int](capacity)
	
	// Pre-fill some data
	for i := 0; i < capacity; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}

	b.RunParallel(func(pb *testing.PB) {
		r := rand.New(rand.NewSource(42))
		for pb.Next() {
			key := fmt.Sprintf("key-%d", r.Intn(capacity*2))
			if r.Intn(10) < 2 {
				c.Set(key, r.Intn(1000))
			} else {
				c.Get(key)
			}
		}
	})
}

func BenchmarkCache_Has(b *testing.B) {
	c := New[string, int](b.N)
	for i := 0; i < b.N; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Has(fmt.Sprintf("key-%d", i))
	}
}

func BenchmarkCache_Peek(b *testing.B) {
	c := New[string, int](b.N)
	for i := 0; i < b.N; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Peek(fmt.Sprintf("key-%d", i))
	}
}

func BenchmarkCache_Concurrent_ReadHeavy(b *testing.B) {
	capacity := 10000
	c := New[string, int](capacity)

	// Pre-fill some data
	for i := 0; i < capacity; i++ {
		c.Set(fmt.Sprintf("key-%d", i), i)
	}

	b.RunParallel(func(pb *testing.PB) {
		r := rand.New(rand.NewSource(42))
		for pb.Next() {
			key := fmt.Sprintf("key-%d", r.Intn(capacity))
			// 95% reads (Peek/Has), 5% writes
			switch r.Intn(20) {
			case 0:
				c.Set(key, r.Intn(1000))
			default:
				if r.Intn(2) == 0 {
					c.Peek(key)
				} else {
					c.Has(key)
				}
			}
		}
	})
}
