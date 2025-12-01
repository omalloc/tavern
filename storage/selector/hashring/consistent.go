package hashring

import (
	"errors"
	"hash/crc32"
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

type uints []uint32

// Len returns the length of the uints array.
func (x uints) Len() int { return len(x) }

// Less returns true if element i is less than element j.
func (x uints) Less(i, j int) bool { return x[i] < x[j] }

// Swap exchanges elements i and j.
func (x uints) Swap(i, j int) { x[i], x[j] = x[j], x[i] }

// ErrEmptyCircle is the error returned when trying to get an element when nothing has been added to hash.
var ErrEmptyCircle = errors.New("empty circle")

type Node interface {
	ID() string
	Weight() int
}

// Consistent holds the information about the members of the consistent hash circle.
type Consistent struct {
	sync.RWMutex // tavern 只有初始化时才写，其他都是读，故不需要用锁

	circles          map[uint32]Node
	members          map[string]bool
	sortedHashes     uints
	NumberOfReplicas int
	count            int64
	UseFnv           bool
}

// NewConsistent creates a new Consistent object with a default setting of 20 replicas for each entry.
// To change the number of replicas, set NumberOfReplicas before adding entries.
func NewConsistent(caches []Node, replicas int) *Consistent {
	c := new(Consistent)
	c.NumberOfReplicas = replicas
	c.circles = make(map[uint32]Node)
	c.members = make(map[string]bool)
	c.UseFnv = true
	c.Set(caches)
	return c
}

// eltKey generates a string key for an element with an index.
func (c *Consistent) eltKey(elt string, idx int, weigth int) string {
	return strconv.Itoa(idx) + "|" + strconv.Itoa(weigth) + "|" + elt
}

func (c *Consistent) SetNumberOfReplicas(num int) {
	if num < 1 {
		num = 1
	}
	c.NumberOfReplicas = num
}

// Add inserts a string element in the consistent hash.
func (c *Consistent) Add(cache Node, weigth int) {
	// c.Lock()
	// defer c.Unlock()
	c.add(cache, weigth)
}

// need c.Lock() before calling
func (c *Consistent) add(cache Node, weigth int) {
	elt := cache.ID()
	for i := 0; i < c.NumberOfReplicas; i++ {
		for j := 0; j < weigth; j++ {
			c.circles[c.hashKey(c.eltKey(elt, i, j))] = cache
		}
	}
	c.members[elt] = true
	c.updateSortedHashes()
	c.count++
}

// Remove removes an element from the hash.
func (c *Consistent) Remove(cache Node, weigth int) {
	c.Lock()
	defer c.Unlock()

	c.remove(cache.ID(), weigth)
}

// need c.Lock() before calling
func (c *Consistent) remove(elt string, weigth int) {
	for i := 0; i < c.NumberOfReplicas; i++ {
		for j := 0; j < weigth; j++ {
			delete(c.circles, c.hashKey(c.eltKey(elt, i, j)))
		}
	}
	delete(c.members, elt)
	c.updateSortedHashes()
	c.count--
}

// Set sets all the elements in the hash.  If there are existing elements not
// present in elts, they will be removed.
func (c *Consistent) Set(caches []Node) {
	// c.Lock()
	// defer c.Unlock()
	elts := make(map[string]Node, len(caches))
	for _, cache := range caches {
		elts[cache.ID()] = cache
	}
	for k := range c.members {
		found := false
		for _, v := range elts {
			if k == v.ID() {
				found = true
				break
			}
		}
		if !found {
			c.remove(k, 1)
		}
	}
	for _, v := range elts {
		_, exists := c.members[v.ID()]
		if exists {
			continue
		}
		c.add(v, v.Weight())
	}
}

func (c *Consistent) Members() []string {
	c.RLock()
	defer c.RUnlock()

	var m []string
	for k := range c.members {
		m = append(m, k)
	}
	return m
}

// Get returns an element close to where name hashes to in the circle.
func (c *Consistent) Get(name string) (Node, error) {
	// c.RLock()
	// defer c.RUnlock()
	if len(c.circles) == 0 {
		return nil, ErrEmptyCircle
	}
	key := c.hashKey(name)
	i := c.search(key)
	cache := c.circles[c.sortedHashes[i]]
	return cache, nil
}

func (c *Consistent) search(key uint32) int {
	f := func(x int) bool {
		return c.sortedHashes[x] > key
	}
	i := sort.Search(len(c.sortedHashes), f)
	if i >= len(c.sortedHashes) {
		i = 0
	}
	return i
}

// GetN returns the N closest distinct elements to the name input in the circle.
func (c *Consistent) GetN(name string, n int) ([]Node, error) {
	if len(c.circles) == 0 {
		return nil, ErrEmptyCircle
	}

	if c.count < int64(n) {
		n = int(c.count)
	}

	var (
		key   = c.hashKey(name)
		i     = c.search(key)
		start = i
		res   = make([]Node, 0, n)
		elem  = c.circles[c.sortedHashes[i]]
	)

	res = append(res, elem)

	if len(res) == n {
		return res, nil
	}

	for i = start + 1; i != start; i++ {
		if i >= len(c.sortedHashes) {
			i = 0
		}
		elem = c.circles[c.sortedHashes[i]]
		if !sliceContainsMember(res, elem) {
			res = append(res, elem)
		}
		if len(res) == n {
			break
		}
	}

	return res, nil
}

func (c *Consistent) hashKey(key string) uint32 {
	if c.UseFnv {
		return c.hashKeyFnv(key)
	}
	return c.hashKeyCRC32(key)
}

func (c *Consistent) hashKeyCRC32(key string) uint32 {
	if len(key) < 64 {
		var scratch [64]byte
		copy(scratch[:], key)
		return crc32.ChecksumIEEE(scratch[:len(key)])
	}
	return crc32.ChecksumIEEE([]byte(key))
}

func (c *Consistent) hashKeyFnv(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}

func (c *Consistent) updateSortedHashes() {
	hashes := c.sortedHashes[:0]
	// reallocate if we're holding on to too much (1/4th)
	if cap(c.sortedHashes)/(c.NumberOfReplicas*4) > len(c.circles) {
		hashes = nil
	}
	for k := range c.circles {
		hashes = append(hashes, k)
	}
	sort.Sort(hashes)
	c.sortedHashes = hashes
}

func sliceContainsMember(set []Node, member Node) bool {
	for _, m := range set {
		if m.ID() == member.ID() {
			return true
		}
	}
	return false
}
