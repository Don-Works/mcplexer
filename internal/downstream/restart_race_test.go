package downstream

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestEvictGetOrStartRace(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	m := NewManager(db, nil)

	id := "race-evict-getorstart"
	url := "http://localhost:0"
	srv := &store.DownstreamServer{
		ID:            id,
		Name:          id,
		Transport:     "http",
		URL:           &url,
		ToolNamespace: id,
		Discovery:     "dynamic",
		Source:        "test",
	}
	if err := m.store.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer: %v", err)
	}

	key := InstanceKey{ServerID: id, AuthScopeID: ""}

	var startCalls atomic.Int32
	var evictCalls atomic.Int32

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Either getOrStart or evict, alternating.
			if i%2 == 0 {
				startCalls.Add(1)
				_, _ = m.getOrStart(ctx, key)
			} else {
				evictCalls.Add(1)
				m.evict(key)
			}
		}(i)
	}
	wg.Wait()

	if startCalls.Load() > 0 || evictCalls.Load() > 0 {
		t.Logf("startCalls=%d evictCalls=%d", startCalls.Load(), evictCalls.Load())
	}
}

func TestEvictDoesNotDuplicateOrphan(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	m := NewManager(db, nil)

	id := "race-evict-no-orphan"
	url := "http://localhost:0"
	srv := &store.DownstreamServer{
		ID:            id,
		Name:          id,
		Transport:     "http",
		URL:           &url,
		ToolNamespace: id,
		Discovery:     "dynamic",
		Source:        "test",
	}
	if err := m.store.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer: %v", err)
	}

	key := InstanceKey{ServerID: id, AuthScopeID: ""}

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Rapidly interleave getOrStart and evict.
			m.getOrStart(ctx, key)
			m.evict(key)
		}()
	}
	wg.Wait()

	instances := m.ListInstances()
	if len(instances) > 1 {
		t.Errorf("expected at most 1 instance, got %d", len(instances))
	}
}
