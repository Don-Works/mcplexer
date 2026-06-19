package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCache_GetSet(t *testing.T) {
	c := New[string, int](10, time.Minute)

	// Miss
	_, ok := c.Get("a")
	if ok {
		t.Fatal("expected miss on empty cache")
	}

	// Set and hit
	c.Set("a", 42)
	v, ok := c.Get("a")
	if !ok || v != 42 {
		t.Fatalf("Get(a) = %d, %v; want 42, true", v, ok)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c := New[string, int](10, 10*time.Millisecond)
	c.Set("a", 1)

	v, ok := c.Get("a")
	if !ok || v != 1 {
		t.Fatal("expected hit before TTL")
	}

	time.Sleep(15 * time.Millisecond)
	_, ok = c.Get("a")
	if ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestCache_CustomTTL(t *testing.T) {
	c := New[string, int](10, time.Hour)
	c.SetWithTTL("short", 1, 10*time.Millisecond)
	c.SetWithTTL("long", 2, time.Hour)

	time.Sleep(15 * time.Millisecond)

	_, ok := c.Get("short")
	if ok {
		t.Fatal("expected miss for short TTL")
	}

	v, ok := c.Get("long")
	if !ok || v != 2 {
		t.Fatal("expected hit for long TTL")
	}
}

func TestCache_LRUEviction(t *testing.T) {
	c := New[string, int](3, time.Minute)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	// Access "a" to move it to front.
	c.Get("a")

	// Adding "d" should evict "b" (least recently used).
	c.Set("d", 4)

	if _, ok := c.Get("b"); ok {
		t.Fatal("expected 'b' to be evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected 'a' to survive (recently accessed)")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("expected 'c' to survive")
	}
	if _, ok := c.Get("d"); !ok {
		t.Fatal("expected 'd' to survive")
	}
}

func TestCache_UpdateExisting(t *testing.T) {
	c := New[string, int](10, time.Minute)

	c.Set("a", 1)
	c.Set("a", 2)

	v, ok := c.Get("a")
	if !ok || v != 2 {
		t.Fatalf("Get(a) = %d, %v; want 2, true", v, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d; want 1", c.Len())
	}
}

func TestCache_Invalidate(t *testing.T) {
	c := New[string, int](10, time.Minute)
	c.Set("a", 1)
	c.Set("b", 2)

	c.Invalidate("a")
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected 'a' to be invalidated")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("expected 'b' to survive")
	}
}

func TestCache_InvalidateFunc(t *testing.T) {
	c := New[string, int](10, time.Minute)
	c.Set("server1:tool1", 1)
	c.Set("server1:tool2", 2)
	c.Set("server2:tool1", 3)

	// Invalidate all server1 entries.
	c.InvalidateFunc(func(k string) bool {
		return len(k) >= 8 && k[:8] == "server1:"
	})

	if _, ok := c.Get("server1:tool1"); ok {
		t.Fatal("expected server1:tool1 to be invalidated")
	}
	if _, ok := c.Get("server1:tool2"); ok {
		t.Fatal("expected server1:tool2 to be invalidated")
	}
	if _, ok := c.Get("server2:tool1"); !ok {
		t.Fatal("expected server2:tool1 to survive")
	}
}

func TestCache_Flush(t *testing.T) {
	c := New[string, int](10, time.Minute)
	c.Set("a", 1)
	c.Set("b", 2)

	c.Flush()
	if c.Len() != 0 {
		t.Fatalf("Len after Flush = %d; want 0", c.Len())
	}
}

func TestCache_GetOrLoad(t *testing.T) {
	c := New[string, int](10, time.Minute)
	loads := 0

	loader := func() (int, error) {
		loads++
		return 42, nil
	}

	// First call loads.
	v, err := c.GetOrLoad("a", loader)
	if err != nil || v != 42 {
		t.Fatalf("GetOrLoad = %d, %v; want 42, nil", v, err)
	}
	if loads != 1 {
		t.Fatalf("loads = %d; want 1", loads)
	}

	// Second call hits cache.
	v, err = c.GetOrLoad("a", loader)
	if err != nil || v != 42 {
		t.Fatalf("GetOrLoad = %d, %v; want 42, nil", v, err)
	}
	if loads != 1 {
		t.Fatalf("loads = %d; want 1 (should not reload)", loads)
	}
}

func TestCache_GetOrLoad_Error(t *testing.T) {
	c := New[string, int](10, time.Minute)
	errDB := errors.New("db error")

	v, err := c.GetOrLoad("a", func() (int, error) {
		return 0, errDB
	})
	if !errors.Is(err, errDB) {
		t.Fatalf("err = %v; want %v", err, errDB)
	}
	if v != 0 {
		t.Fatalf("value = %d; want 0", v)
	}

	// Should not cache errors.
	if c.Len() != 0 {
		t.Fatal("error result should not be cached")
	}
}

func TestCache_GetOrLoad_Singleflight(t *testing.T) {
	c := New[string, int](10, time.Minute)
	var loadCount atomic.Int32

	loader := func() (int, error) {
		loadCount.Add(1)
		time.Sleep(50 * time.Millisecond)
		return 99, nil
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.GetOrLoad("key", loader)
			if err != nil || v != 99 {
				t.Errorf("GetOrLoad = %d, %v; want 99, nil", v, err)
			}
		}()
	}
	wg.Wait()

	if n := loadCount.Load(); n != 1 {
		t.Fatalf("load count = %d; want 1 (singleflight)", n)
	}
}

func TestCache_ConcurrentAccess(t *testing.T) {
	c := New[int, int](100, time.Minute)
	var wg sync.WaitGroup

	// Concurrent writers.
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.Set(n, n*10)
		}(i)
	}

	// Concurrent readers.
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.Get(n)
		}(i)
	}

	// Concurrent invalidations.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.InvalidateFunc(func(k int) bool { return k%7 == 0 })
		}()
	}

	wg.Wait()
	// No panic = test passes.
}

func TestCache_Stats(t *testing.T) {
	c := New[string, int](3, time.Minute)

	c.Set("a", 1)
	c.Set("b", 2)
	c.Get("a")       // hit
	c.Get("missing") // miss

	s := c.Stats()
	if s.Hits != 1 {
		t.Errorf("Hits = %d; want 1", s.Hits)
	}
	if s.Misses != 1 {
		t.Errorf("Misses = %d; want 1", s.Misses)
	}
	if s.Entries != 2 {
		t.Errorf("Entries = %d; want 2", s.Entries)
	}
	if s.HitRate != 0.5 {
		t.Errorf("HitRate = %f; want 0.5", s.HitRate)
	}

	// Trigger evictions.
	c.Set("c", 3)
	c.Set("d", 4) // evicts one
	s = c.Stats()
	if s.Evictions != 1 {
		t.Errorf("Evictions = %d; want 1", s.Evictions)
	}
}

func TestCache_ResetStats(t *testing.T) {
	c := New[string, int](10, time.Minute)
	c.Set("a", 1)
	c.Get("a")
	c.Get("b")

	c.ResetStats()
	s := c.Stats()
	if s.Hits != 0 || s.Misses != 0 {
		t.Fatalf("stats not reset: %+v", s)
	}
}
