package usage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type blockingAllowanceCollector struct {
	calls   atomic.Int32
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (c *blockingAllowanceCollector) Fetch(
	context.Context,
	store.SourceConfig,
) (store.CollectorResult, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.started) })
	<-c.release
	return store.CollectorResult{Snapshot: store.ProviderSnapshot{
		Status:  store.StatusOK,
		Windows: []store.UsageWindow{{ID: "live", Unit: store.UnitPercent}},
	}}, nil
}

func TestConcurrentForcedSnapshotsShareProviderProbe(t *testing.T) {
	collector := &blockingAllowanceCollector{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := &Service{
		Collectors: map[string]ProviderCollector{
			store.ProviderMiniMax: collector,
		},
	}
	config := []store.SourceConfig{apiConfig(store.ProviderMiniMax, "scope")}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = service.Snapshot(context.Background(), config, 30, true)
		}()
	}
	close(start)
	<-collector.started
	time.Sleep(50 * time.Millisecond)
	close(collector.release)
	wg.Wait()
	if calls := collector.calls.Load(); calls != 1 {
		t.Fatalf("collector calls = %d, want one shared probe", calls)
	}
}
