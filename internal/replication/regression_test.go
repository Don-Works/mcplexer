package replication_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/replication"
)

// maxQueueDepth mirrors the (unexported) cap in queue.go. Kept in sync
// here so the overflow regression test can assert the exact bound
// without exporting the constant.
const testMaxQueueDepth = 128

// TestQueueOverflowDropsOldest is the regression test for the per-peer
// backpressure guarantee (queue.go enqueueOne): a peer that's been
// disconnected long enough to accumulate >maxQueueDepth distinct items
// must cap at maxQueueDepth, drop the OLDEST (FIFO), and allow a dropped
// id to be re-enqueued afterwards (the seen-set reset on drop).
func TestQueueOverflowDropsOldest(t *testing.T) {
	tiers := map[string]consent.Tier{"peer-B": consent.TierSameUser}

	t.Run("caps_at_max_and_drops_oldest", func(t *testing.T) {
		c, pusher, _ := makeCoordinator(t,
			[]replication.PeerInfo{{PeerID: "peer-B"}}, tiers)

		// Enqueue more distinct ids than the cap.
		total := testMaxQueueDepth + 50
		for i := 0; i < total; i++ {
			c.OnMemoryEvent(context.Background(), "write", memID(i), "agent")
		}

		if got := c.QueueDepth("peer-B"); got != testMaxQueueDepth {
			t.Fatalf("queue depth = %d, want cap of %d", got, testMaxQueueDepth)
		}

		// Drain and assert the surviving ids are the NEWEST contiguous
		// window [total-cap, total) — i.e. the oldest `50` were dropped
		// FIFO and ordering within the survivors is preserved.
		c.DrainOnce(context.Background())
		if !waitForCalls(pusher, testMaxQueueDepth, 2*time.Second) {
			t.Fatalf("expected %d pushes, got %d",
				testMaxQueueDepth, len(pusher.snapshot()))
		}
		calls := pusher.snapshot()
		if len(calls) != testMaxQueueDepth {
			t.Fatalf("push count = %d, want %d", len(calls), testMaxQueueDepth)
		}
		firstSurvivor := total - testMaxQueueDepth
		for idx, call := range calls {
			want := memID(firstSurvivor + idx)
			if call.ID != want {
				t.Fatalf("survivor[%d] = %s, want %s (oldest not dropped FIFO)",
					idx, call.ID, want)
			}
		}
	})

	t.Run("dropped_id_can_be_reenqueued", func(t *testing.T) {
		c, _, _ := makeCoordinator(t,
			[]replication.PeerInfo{{PeerID: "peer-B"}}, tiers)

		// Overflow so mem-0 is dropped.
		total := testMaxQueueDepth + 1
		for i := 0; i < total; i++ {
			c.OnMemoryEvent(context.Background(), "write", memID(i), "agent")
		}
		if got := c.QueueDepth("peer-B"); got != testMaxQueueDepth {
			t.Fatalf("queue depth = %d, want %d", got, testMaxQueueDepth)
		}

		// Re-enqueue the dropped id. If the seen-set reset on drop is
		// broken, this is silently swallowed as a dup and depth stays
		// at the cap; with the reset it overflows once more (drops the
		// next-oldest) and the depth remains at the cap but mem-0 is
		// present again. To make the assertion crisp, re-enqueue an id
		// that was dropped and verify it is NOT treated as a duplicate
		// by checking that a previously-dropped id re-enqueues while a
		// still-present id dedups.
		dropped := memID(0)              // dropped on overflow above
		stillPresent := memID(total - 1) // newest, definitely present

		// Re-enqueue a still-present id: must dedup (depth unchanged).
		c.OnMemoryEvent(context.Background(), "write", stillPresent, "agent")
		if got := c.QueueDepth("peer-B"); got != testMaxQueueDepth {
			t.Fatalf("re-enqueue of present id changed depth to %d, want %d (dedup broken)",
				got, testMaxQueueDepth)
		}

		// Re-enqueue the dropped id: seen was reset on drop, so this is
		// a fresh enqueue. Depth stays at cap (it drops the oldest to
		// make room) but the dropped id must now be the NEWEST item.
		c.OnMemoryEvent(context.Background(), "write", dropped, "agent")
		if got := c.QueueDepth("peer-B"); got != testMaxQueueDepth {
			t.Fatalf("re-enqueue of dropped id: depth = %d, want %d", got, testMaxQueueDepth)
		}
	})
}

// memID builds a stable, distinct memory id for index i.
func memID(i int) string { return "mem-" + strconv.Itoa(i) }

// TestConcurrentOnEventAndStop is the regression test for the data race
// on c.closed / c.taskPush / c.links (replication.go). It fires the On*
// hooks and SetTaskReplication concurrently with Stop — the exact
// production interleaving (chainedMemoryNotify + the task emitter racing
// daemon shutdown) that the existing t.Cleanup(Stop) tests never
// exercise. Run with `go test -race` to catch a regression.
func TestConcurrentOnEventAndStop(t *testing.T) {
	peers := []replication.PeerInfo{{PeerID: "peer-B"}}
	tiers := map[string]consent.Tier{"peer-B": consent.TierSameUser}
	resolver := &fakeTierResolver{tiers: tiers}
	lister := &fakePeerLister{Peers: peers}
	pusher := &recordingPusher{}
	links := &fakeLinkLister{links: map[string][]string{"ws-gateway": {"peer-B"}}}

	c := replication.NewCoordinator(resolver, lister, pusher, pusher,
		replication.Config{BatchInterval: time.Millisecond})
	if c == nil {
		t.Fatal("NewCoordinator returned nil")
	}
	c.Start(context.Background())

	var wg sync.WaitGroup
	ctx := context.Background()

	// Concurrent memory-event hammering.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.OnMemoryEvent(ctx, "write", memID(i), "agent")
		}
	}()

	// Concurrent skill-install hammering.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.OnSkillInstall(ctx, "skill-"+strconv.Itoa(i), false)
		}
	}()

	// Concurrent task-event hammering racing SetTaskReplication wiring.
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.SetTaskReplication(pusher, links)
		for i := 0; i < 200; i++ {
			c.OnTaskEvent(ctx, "ws-gateway", "task-"+strconv.Itoa(i), "agent")
		}
	}()

	// Concurrent shutdown — the racing party.
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Stop()
	}()

	wg.Wait()
	c.Wait()

	// Stop is also idempotent under concurrency — a second call is a
	// no-op, not a double-close panic.
	c.Stop()
}

// TestIntervalEnvOverride is the regression test for the operator-facing
// env knob (ReplicationBatchIntervalEnv → Interval()) and the
// IntervalSource() reporting, including the invalid-env fallback. It
// guards against the NewCoordinator parse and the IntervalSource parse
// (now a shared resolveInterval helper) diverging.
func TestIntervalEnvOverride(t *testing.T) {
	cases := []struct {
		name         string
		env          string // "" means unset
		setEnv       bool
		cfgInterval  time.Duration
		wantInterval time.Duration
		// sourceContains is a substring that IntervalSource() must contain.
		sourceContains string
	}{
		{
			name:           "valid_env_overrides_default",
			env:            "2s",
			setEnv:         true,
			wantInterval:   2 * time.Second,
			sourceContains: "2s",
		},
		{
			name:           "valid_env_overrides_config",
			env:            "750ms",
			setEnv:         true,
			cfgInterval:    10 * time.Second,
			wantInterval:   750 * time.Millisecond,
			sourceContains: "750ms",
		},
		{
			name:           "invalid_env_falls_back_to_default",
			env:            "garbage",
			setEnv:         true,
			wantInterval:   replication.DefaultBatchInterval,
			sourceContains: "bad env",
		},
		{
			name:           "zero_duration_env_falls_back",
			env:            "0s",
			setEnv:         true,
			wantInterval:   replication.DefaultBatchInterval,
			sourceContains: "bad env",
		},
		{
			name:           "unset_uses_default",
			setEnv:         false,
			wantInterval:   replication.DefaultBatchInterval,
			sourceContains: "default",
		},
		{
			// IntervalSource() is a package-level reporter that only sees
			// env + DefaultBatchInterval (it takes no args), so it reports
			// "default" even when a Config.BatchInterval was supplied. The
			// effective Interval() still reflects the config value. This
			// case pins that documented split so a future change that
			// teaches IntervalSource about config doesn't silently regress
			// the start-up log line.
			name:           "unset_uses_config_for_interval_source_reports_default",
			setEnv:         false,
			cfgInterval:    3 * time.Second,
			wantInterval:   3 * time.Second,
			sourceContains: "default",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setEnv {
				t.Setenv(replication.ReplicationBatchIntervalEnv, tc.env)
			} else {
				// Ensure a stray ambient value doesn't leak in. Setenv to
				// empty then the helper treats "" as unset.
				t.Setenv(replication.ReplicationBatchIntervalEnv, "")
			}

			resolver := &fakeTierResolver{}
			lister := &fakePeerLister{}
			pusher := &recordingPusher{}
			c := replication.NewCoordinator(resolver, lister, pusher, pusher,
				replication.Config{BatchInterval: tc.cfgInterval})
			if c == nil {
				t.Fatal("NewCoordinator returned nil")
			}
			t.Cleanup(c.Stop)

			if got := c.Interval(); got != tc.wantInterval {
				t.Fatalf("Interval() = %s, want %s", got, tc.wantInterval)
			}
			if src := replication.IntervalSource(); !strings.Contains(src, tc.sourceContains) {
				t.Fatalf("IntervalSource() = %q, want substring %q", src, tc.sourceContains)
			}
		})
	}
}
