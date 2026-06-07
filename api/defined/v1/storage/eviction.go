package storage

type Eviction[K comparable, V any] struct {
	Key   K
	Value V
}

type CacheReplacementPolicy[K comparable, V any] interface{
	SetEvictionCh(ch chan<- Eviction[K, V])
	
	Has(key K) bool
	Get(key K) *V
	Peek(key K) *V
	Set(key K, value V)
	Len() int
	TopK(k int) []K
}