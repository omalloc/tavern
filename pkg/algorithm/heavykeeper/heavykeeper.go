package heavykeeper

import (
	"hash/fnv"
	"math"
	"math/rand"
	"sync"
	"time"
)

// HeavyKeeper is a probabilistic data structure for top-k items.
type HeavyKeeper struct {
	buckets [][]bucket
	depth   int
	width   int
	decay   float64
	r       *rand.Rand
	mu      sync.RWMutex
}

type bucket struct {
	fingerprint uint64
	count       uint32
}

// New creates a new HeavyKeeper.
// depth: number of arrays (hash functions)
// width: number of buckets per array
// decay: probability of decay (0.9 means 90% chance to decay)
func New(depth, width int, decay float64) *HeavyKeeper {
	hk := &HeavyKeeper{
		buckets: make([][]bucket, depth),
		depth:   depth,
		width:   width,
		decay:   decay,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	for i := range hk.buckets {
		hk.buckets[i] = make([]bucket, width)
	}

	return hk
}

// Add adds a key to the HeavyKeeper.
func (hk *HeavyKeeper) Add(key []byte) {
	hk.mu.Lock()
	defer hk.mu.Unlock()

	fingerprint := hk.hash(key)

	// Use double hashing for multiple hash functions
	// h1 = fingerprint
	// h2 = fnv(key)
	// idx = (h1 + i*h2) % width
	h2 := hk.hash2(key)

	for i := 0; i < hk.depth; i++ {
		idx := (fingerprint + uint64(i)*h2) % uint64(hk.width)
		b := &hk.buckets[i][idx]

		if b.count == 0 {
			b.fingerprint = fingerprint
			b.count = 1
			continue
		}

		if b.fingerprint == fingerprint {
			b.count++
			continue
		}

		// Decay
		if hk.r.Float64() < math.Pow(hk.decay, float64(b.count)) {
			b.count--
			if b.count == 0 {
				b.fingerprint = fingerprint
				b.count = 1
			}
		}
	}
}

// Query returns the estimated count for the key.
func (hk *HeavyKeeper) Query(key []byte) uint32 {
	hk.mu.RLock()
	defer hk.mu.RUnlock()

	fingerprint := hk.hash(key)
	h2 := hk.hash2(key)
	var maxCount uint32

	for i := 0; i < hk.depth; i++ {
		idx := (fingerprint + uint64(i)*h2) % uint64(hk.width)
		b := &hk.buckets[i][idx]

		if b.fingerprint == fingerprint {
			if b.count > maxCount {
				maxCount = b.count
			}
		}
	}

	return maxCount
}

// Clear resets the HeavyKeeper.
func (hk *HeavyKeeper) Clear() {
	hk.mu.Lock()
	defer hk.mu.Unlock()

	for i := range hk.buckets {
		for j := range hk.buckets[i] {
			hk.buckets[i][j].count = 0
			hk.buckets[i][j].fingerprint = 0
		}
	}
}

func (hk *HeavyKeeper) hash(key []byte) uint64 {
	h := fnv.New64a()
	h.Write(key)
	return h.Sum64()
}

func (hk *HeavyKeeper) hash2(key []byte) uint64 {
	// Simple secondary hash: fnv with salt or just different algo
	// Using FNV-1 (not 1a) or just rotate?
	// Let's use a simple mix.
	h := uint64(2166136261)
	for _, c := range key {
		h *= 16777619
		h ^= uint64(c)
	}
	// salt
	h ^= 0x9e3779b97f4a7c15
	return h
}
