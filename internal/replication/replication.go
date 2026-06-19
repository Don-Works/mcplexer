// Package replication implements the Tier-1 (same-user) silent
// replication coordinator.
//
// # Why
//
// The cross-peer share protocols (memory_share, skill_share) are
// request/offer/accept: peer A produces an offer, peer B has to
// actively pull. For Tier-1 same-user pairs the UX promise is "your
// other machine just works" — a memory written on the laptop should
// silently appear on the desktop within a few seconds, no manual sync.
//
// This package subscribes to local memory writes + skill installs,
// fans them out to every Tier-1 same-user paired peer, and pushes them
// over the existing p2p share protocols. Tier 2/3 stays manual.
//
// # Boundaries
//
// The coordinator depends on three narrow interfaces:
//
//   - TierResolver        — consent.Resolver-shaped, classifies a peer's
//     trust tier. Only TierSameUser triggers replication.
//   - PeerLister          — store.P2PPeerStore-shaped, returns the
//     active paired-peer list + their granted scopes.
//   - MemoryPusher        — opens a libp2p stream to one peer and
//     pushes a memory by id. The daemon supplies a real implementation
//     backed by p2p.MemoryShareService.
//   - SkillPusher         — same posture for skills.
//
// The coordinator NEVER imports internal/p2p directly: that package is
// build-tag gated (`-tags p2p`) and the slim build wires both pushers
// as no-ops so the coordinator itself compiles + runs in both modes.
//
// # Opt-out flag
//
// Default behaviour is "auto-replicate every Tier-1 same-user peer".
// The UX promise of same-user pairing is silent-by-design. An operator
// can opt OUT per-peer by granting the scope ReplicationOptOutScope on
// that peer — the coordinator sees the scope and skips that peer.
// Granting/revoking the scope uses the existing mesh__grant_peer_scope
// surface so no new admin tool is needed.
//
// # Batching
//
// Each peer has its own ring-buffer queue. A batch interval ticker
// (default DefaultBatchInterval, override via env
// ReplicationBatchIntervalEnv or constructor option) drains every
// queue once per tick and dispatches via the pusher. The push happens
// in a fresh goroutine per peer per tick so a slow peer never blocks
// the others.
//
// # Echo prevention
//
// Peer-origin events (memory.Event.Source == "peer") are dropped at
// the OnMemoryEvent boundary so receiving a memory from peer A and
// re-broadcasting it to peer B never happens.
package replication

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Coordinator owns the per-peer queues + the batch ticker. Safe for
// concurrent use — OnMemoryEvent / OnSkillInstall are typically called
// from the memory.Service.Notify hook + the skill-install code path
// concurrently with the ticker goroutine.
type Coordinator struct {
	tiers    TierResolver
	peers    PeerLister
	memPush  MemoryPusher
	skillPsh SkillPusher
	logger   *slog.Logger
	interval time.Duration

	// Task replication is opt-in + nil-safe (wired post-construction via
	// SetTaskReplication so NewCoordinator's signature — and every
	// existing call site — is untouched). Both must be non-nil for
	// OnTaskEvent to do anything.
	taskPush TaskPusher
	links    LinkLister

	mu     sync.Mutex
	queues map[string]*peerQueue // peerID -> queue

	// closed is an atomic.Bool so the On*Event hot paths can read the
	// shutdown flag without taking c.mu (which Stop holds while closing
	// stopCh). Stop is the sole writer; the hooks are readers.
	closed atomic.Bool

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewCoordinator wires the coordinator. The caller must call Start
// for the batch ticker to fire; the queue surface (OnMemoryEvent /
// OnSkillInstall) is usable immediately after construction so wiring
// order between memory.Service.Notify and replication.Coordinator
// doesn't matter.
//
// Returns nil if any of tiers, peers, memPush, skillPsh is nil — the
// daemon then logs "replication disabled" and life continues without
// auto-rep (manual share still works).
func NewCoordinator(
	tiers TierResolver,
	peers PeerLister,
	memPush MemoryPusher,
	skillPsh SkillPusher,
	cfg Config,
) *Coordinator {
	if tiers == nil || peers == nil || memPush == nil || skillPsh == nil {
		return nil
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	base := cfg.BatchInterval
	if base <= 0 {
		base = DefaultBatchInterval
	}
	interval, _, badEnv := resolveInterval(base)
	if badEnv != "" {
		logger.Warn("replication: invalid batch interval env",
			"env", ReplicationBatchIntervalEnv, "value", badEnv)
	}
	return &Coordinator{
		tiers:    tiers,
		peers:    peers,
		memPush:  memPush,
		skillPsh: skillPsh,
		logger:   logger,
		interval: interval,
		queues:   make(map[string]*peerQueue),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start kicks off the batch ticker. Idempotent — calling twice on the
// same coordinator is safe (the second call returns immediately).
// Stop cancels the ticker; Wait blocks until the goroutine exits.
func (c *Coordinator) Start(ctx context.Context) {
	if c == nil {
		return
	}
	go c.loop(ctx)
}

// Stop signals the ticker to exit. Idempotent.
func (c *Coordinator) Stop() {
	if c == nil {
		return
	}
	// CompareAndSwap makes Stop idempotent without holding c.mu across
	// the channel close: the first caller flips false→true and closes
	// stopCh; concurrent/repeat callers see true and return.
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	close(c.stopCh)
}

// Wait blocks until the ticker goroutine has exited. Safe to call
// before Start (returns immediately because doneCh is buffered+lazy).
func (c *Coordinator) Wait() {
	if c == nil {
		return
	}
	<-c.doneCh
}

// Interval returns the resolved batch interval. Useful for tests +
// the dashboard's "next drain in Xs" widget.
func (c *Coordinator) Interval() time.Duration { return c.interval }

// OnMemoryEvent is the hook the memory.Service.Notify chain calls
// after a successful write/invalidate/etc. Only kind="write" is
// replicated today; everything else is dropped silently. Peer-origin
// events (Source=="peer") are also dropped — that's how the
// coordinator avoids re-broadcasting a memory it just received.
//
// Errors during peer enumeration / queueing are logged but never
// returned: replication is best-effort and must never block the
// originating write path. ctx is honoured for cancellation only.
func (c *Coordinator) OnMemoryEvent(ctx context.Context, kind, memoryID, source string) {
	if c == nil || c.closed.Load() {
		return
	}
	if kind != kindMemoryWrite {
		return
	}
	if source == "peer" {
		// Don't replicate what we just received from a peer — closes
		// the obvious echo loop in same-user pairs.
		return
	}
	if memoryID == "" {
		return
	}
	c.enqueueForTier1Peers(ctx, Item{Kind: EventKindMemory, ID: memoryID})
}

// OnSkillInstall is fired by the daemon's skill-install pathway after
// a successful install. The skill bytes are NOT carried here — the
// pusher resolves them at dispatch time. Receiving-side skill installs
// (HandleIncomingBundle) MUST NOT call this; the daemon's bundle
// receiver path skips it to prevent echo. peerOriginInstall=true is
// the explicit "I just received this from a peer, don't replicate"
// flag for safety even if the wiring forgets.
func (c *Coordinator) OnSkillInstall(ctx context.Context, skillName string, peerOriginInstall bool) {
	if c == nil || c.closed.Load() {
		return
	}
	if peerOriginInstall {
		return
	}
	if skillName == "" {
		return
	}
	c.enqueueForTier1Peers(ctx, Item{Kind: EventKindSkill, ID: skillName})
}

// SetTaskReplication wires the task-replication dependencies
// post-construction. Both must be non-nil to enable task replication;
// passing nil for either (the slim build, or a daemon without the task
// service) leaves OnTaskEvent a no-op. Mirrors the nil-safe Set* idiom
// used across the codebase (tasks.Service.SetEmitter / SetTaskShare).
func (c *Coordinator) SetTaskReplication(push TaskPusher, links LinkLister) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.taskPush = push
	c.links = links
	c.mu.Unlock()
}

// OnTaskEvent is the hook the task Emitter calls after a successful,
// observable task mutation. Unlike memory/skills (which fan out to every
// Tier-1 peer), tasks replicate ONLY to peers explicitly *linked* to the
// task's workspace — the send-side gate that keeps cross-machine task
// sync scoped to declared linked workspaces.
//
// Echo prevention mirrors OnMemoryEvent: a mutation whose origin is a
// peer (source=="peer") is dropped so receiving a replicated task and
// re-pushing it never loops. Errors are logged, never returned —
// replication is best-effort and must not block the mutation path.
func (c *Coordinator) OnTaskEvent(ctx context.Context, workspaceID, taskID, source string) {
	if c == nil || c.closed.Load() {
		return
	}
	// Snapshot the task-replication deps under c.mu — SetTaskReplication
	// writes them under the same lock, so reading them bare here would
	// race the wiring call (which fires concurrently at daemon start-up).
	c.mu.Lock()
	taskPush, links := c.taskPush, c.links
	c.mu.Unlock()
	if taskPush == nil || links == nil {
		return // task replication not wired (slim build / no task service)
	}
	if source == "peer" {
		return // don't replicate what we just received from a peer
	}
	if workspaceID == "" || taskID == "" {
		return
	}
	c.enqueueForLinkedPeers(ctx, workspaceID, Item{
		Kind:        EventKindTask,
		ID:          taskID,
		WorkspaceID: workspaceID,
	})
}

// QueueDepth returns the queue depth for one peer. Test helper; the
// coordinator itself never inspects depths.
func (c *Coordinator) QueueDepth(peerID string) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	q := c.queues[peerID]
	c.mu.Unlock()
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// DrainOnce forces an immediate drain for tests. Returns after every
// per-peer goroutine has been dispatched (but not necessarily after
// the push to the peer has completed — that's intentional, so a slow
// peer never blocks the test).
func (c *Coordinator) DrainOnce(ctx context.Context) {
	if c == nil {
		return
	}
	c.drain(ctx)
}

// resolveInterval is the single source of truth for the env-override
// fallback logic, shared by NewCoordinator and IntervalSource so the two
// can never diverge. It applies ReplicationBatchIntervalEnv on top of
// base and returns the effective interval, a human-readable source
// label, and the raw env value when it was set-but-invalid (empty
// otherwise, so the caller can emit a one-time warning).
func resolveInterval(base time.Duration) (interval time.Duration, source, badEnv string) {
	if base <= 0 {
		base = DefaultBatchInterval
	}
	raw := os.Getenv(ReplicationBatchIntervalEnv)
	if raw == "" {
		return base, baseSource(base), ""
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d, raw, ""
	}
	return base, baseSource(base) + " (bad env: " + raw + ")", raw
}

// baseSource renders the non-env source label. DefaultBatchInterval is
// reported as "default (Ns)"; an explicit Config.BatchInterval as its
// duration string.
func baseSource(base time.Duration) string {
	if base == DefaultBatchInterval {
		return "default (" + strconv.FormatInt(int64(DefaultBatchInterval/time.Second), 10) + "s)"
	}
	return base.String()
}

// IntervalSource returns a human-readable string describing where the
// active batch interval came from. Exported so cmd/mcplexer can log
// it at start-up. Reflects the env override + default-fallback exactly
// as NewCoordinator resolved it.
func IntervalSource() string {
	_, source, _ := resolveInterval(DefaultBatchInterval)
	return source
}
