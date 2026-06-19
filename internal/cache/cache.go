package cache

import (
	"container/list"
	"sync"
	"time"
)

// Cache is a generic in-memory cache with LRU eviction, TTL expiry,
// and built-in singleflight for concurrent loads.
type Cache[K comparable, V any] struct {
	mu         sync.Mutex
	items      map[K]*list.Element
	evictList  *list.List
	maxEntries int
	defaultTTL time.Duration
	stats      Stats

	// singleflight: in-progress loads keyed by cache key
	inflight map[K]*call[V]
}

type entry[K comparable, V any] struct {
	key       K
	value     V
	createdAt time.Time
	expiresAt time.Time
}

type call[V any] struct {
	wg  sync.WaitGroup
	val V
	err error
}

// New creates a cache with the given max entries and default TTL.
func New[K comparable, V any](maxEntries int, defaultTTL time.Duration) *Cache[K, V] {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &Cache[K, V]{
		items:      make(map[K]*list.Element),
		evictList:  list.New(),
		maxEntries: maxEntries,
		defaultTTL: defaultTTL,
		inflight:   make(map[K]*call[V]),
	}
}

// Get retrieves a value from the cache. Returns the value and true if
// found and not expired, or the zero value and false otherwise.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		c.stats.Misses++
		var zero V
		return zero, false
	}

	e := el.Value.(*entry[K, V])
	if time.Now().After(e.expiresAt) {
		c.removeLocked(el)
		c.stats.Misses++
		var zero V
		return zero, false
	}

	c.evictList.MoveToFront(el)
	c.stats.Hits++
	return e.value, true
}

// GetWithAge retrieves a value and its age. Returns the value, the time
// since it was cached, and true if found and not expired.
func (c *Cache[K, V]) GetWithAge(key K) (V, time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		c.stats.Misses++
		var zero V
		return zero, 0, false
	}

	e := el.Value.(*entry[K, V])
	now := time.Now()
	if now.After(e.expiresAt) {
		c.removeLocked(el)
		c.stats.Misses++
		var zero V
		return zero, 0, false
	}

	c.evictList.MoveToFront(el)
	c.stats.Hits++
	return e.value, now.Sub(e.createdAt), true
}

// Set stores a value in the cache with the default TTL.
func (c *Cache[K, V]) Set(key K, value V) {
	c.SetWithTTL(key, value, c.defaultTTL)
}

// SetWithTTL stores a value in the cache with a custom TTL.
func (c *Cache[K, V]) SetWithTTL(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if el, ok := c.items[key]; ok {
		c.evictList.MoveToFront(el)
		e := el.Value.(*entry[K, V])
		e.value = value
		e.createdAt = now
		e.expiresAt = now.Add(ttl)
		return
	}

	e := &entry[K, V]{
		key:       key,
		value:     value,
		createdAt: now,
		expiresAt: now.Add(ttl),
	}
	el := c.evictList.PushFront(e)
	c.items[key] = el

	for c.evictList.Len() > c.maxEntries {
		c.evictOldestLocked()
	}
}

// GetOrLoad returns the cached value for key, or calls loadFn to populate it.
// Concurrent calls for the same key share a single load (singleflight).
func (c *Cache[K, V]) GetOrLoad(key K, loadFn func() (V, error)) (V, error) {
	// Fast path: check cache.
	if v, ok := c.Get(key); ok {
		return v, nil
	}

	// Singleflight: check if another goroutine is already loading.
	c.mu.Lock()
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		cl.wg.Wait()
		if cl.err != nil {
			return cl.val, cl.err
		}
		// The loading goroutine already cached the result; try to get it.
		if v, ok := c.Get(key); ok {
			return v, nil
		}
		return cl.val, nil
	}

	cl := &call[V]{}
	cl.wg.Add(1)
	c.inflight[key] = cl
	c.mu.Unlock()

	// Execute the load function outside the lock.
	cl.val, cl.err = loadFn()
	if cl.err == nil {
		c.Set(key, cl.val)
	}
	cl.wg.Done()

	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()

	return cl.val, cl.err
}

// Invalidate removes a single key from the cache.
func (c *Cache[K, V]) Invalidate(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
}

// InvalidateFunc removes all entries for which predicate returns true.
func (c *Cache[K, V]) InvalidateFunc(predicate func(K) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, el := range c.items {
		if predicate(key) {
			c.removeLocked(el)
		}
	}
}

// Flush removes all entries from the cache.
func (c *Cache[K, V]) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[K]*list.Element)
	c.evictList.Init()
}

// Len returns the number of entries in the cache.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Stats returns a snapshot of cache statistics.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Entries = len(c.items)
	if total := s.Hits + s.Misses; total > 0 {
		s.HitRate = float64(s.Hits) / float64(total)
	}
	return s
}

// ResetStats zeroes the hit/miss/eviction counters.
func (c *Cache[K, V]) ResetStats() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats = Stats{}
}

func (c *Cache[K, V]) removeLocked(el *list.Element) {
	e := el.Value.(*entry[K, V])
	delete(c.items, e.key)
	c.evictList.Remove(el)
}

func (c *Cache[K, V]) evictOldestLocked() {
	el := c.evictList.Back()
	if el == nil {
		return
	}
	c.removeLocked(el)
	c.stats.Evictions++
}
