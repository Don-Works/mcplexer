package replication

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/consent"
)

// maxQueueDepth caps the per-peer queue so a peer that's been
// disconnected for hours doesn't accumulate an unbounded backlog. A
// peer that comes back finds the queue full and pulls the missing
// memories via the existing on-demand request/offer flow. Trade-off:
// silent replication is best-effort, not durable — agents that need
// guaranteed delivery should keep using the explicit share path.
const maxQueueDepth = 128

// dispatchTimeout caps how long a single push to a peer can hold one
// dispatch goroutine. A stuck libp2p stream past this point is
// abandoned; the next write re-enqueues it. 30s matches the existing
// memoryShareReadDeadline + skillShareReadDeadline.
const dispatchTimeout = 30 * time.Second

// itemKey is the per-peer dedup key. Folds in WorkspaceID so a task
// (EventKindTask) is keyed by workspace+id; memory/skill items have an
// empty WorkspaceID so their key is unchanged in spirit (kind::id).
func itemKey(it Item) string {
	return string(it.Kind) + ":" + it.WorkspaceID + ":" + it.ID
}

// peerQueue is the per-peer ring buffer. Bounded by maxQueueDepth;
// older entries are dropped on overflow so a hot writer never blocks.
// Dedup by (kind,id) so two writes to the same memory in one batch
// collapse into one push.
type peerQueue struct {
	mu    sync.Mutex
	items []Item
	seen  map[string]struct{} // kind+":"+id -> sentinel for dedup
}

// enqueueForTier1Peers looks up active paired peers, filters to
// Tier-1, drops peers carrying the opt-out scope, and pushes one
// queue entry per matching peer.
func (c *Coordinator) enqueueForTier1Peers(ctx context.Context, item Item) {
	peers, err := c.peers.ListActivePairedPeers(ctx)
	if err != nil {
		c.logger.Debug("replication: list peers failed",
			"error", err, "item_kind", item.Kind)
		return
	}
	for _, p := range peers {
		if p.PeerID == "" {
			continue
		}
		if p.HasScope(ReplicationOptOutScope) {
			continue
		}
		if c.tiers.TierFor(ctx, p.PeerID) != consent.TierSameUser {
			continue
		}
		c.enqueueOne(p.PeerID, item)
	}
}

// enqueueForLinkedPeers is the task-replication equivalent of
// enqueueForTier1Peers. Instead of fanning to every Tier-1 peer it
// targets only peers explicitly *linked* to the task's local workspace
// (LinkLister). The link is the authorization, so no tier check is
// applied — but the per-peer opt-out scope is still honoured, and
// revoked peers are skipped (ListActivePairedPeers already filters them)
// so a linked-but-revoked peer's queue can't accumulate.
func (c *Coordinator) enqueueForLinkedPeers(ctx context.Context, workspaceID string, item Item) {
	linked, err := c.links.LinkedPeersForWorkspace(ctx, workspaceID)
	if err != nil {
		c.logger.Debug("replication: list linked peers failed",
			"error", err, "workspace_id", workspaceID)
		return
	}
	if len(linked) == 0 {
		return
	}
	linkedSet := make(map[string]struct{}, len(linked))
	for _, id := range linked {
		linkedSet[id] = struct{}{}
	}
	active, err := c.peers.ListActivePairedPeers(ctx)
	if err != nil {
		c.logger.Debug("replication: list peers failed",
			"error", err, "item_kind", item.Kind)
		return
	}
	for _, p := range active {
		if p.PeerID == "" {
			continue
		}
		if _, ok := linkedSet[p.PeerID]; !ok {
			continue
		}
		if p.HasScope(ReplicationOptOutScope) {
			continue
		}
		c.enqueueOne(p.PeerID, item)
	}
}

// enqueueOne appends to the per-peer queue. Dedups by (kind,id) so
// multiple writes to the same memory in one batch fold into one push.
// Drops the oldest item on overflow.
func (c *Coordinator) enqueueOne(peerID string, item Item) {
	c.mu.Lock()
	q := c.queues[peerID]
	if q == nil {
		q = &peerQueue{seen: make(map[string]struct{})}
		c.queues[peerID] = q
	}
	c.mu.Unlock()

	q.mu.Lock()
	defer q.mu.Unlock()
	key := itemKey(item)
	if _, exists := q.seen[key]; exists {
		return
	}
	if len(q.items) >= maxQueueDepth {
		// Drop oldest. Reset seen for the dropped item so it can be
		// re-enqueued on the next write (the receiver will get the
		// fresh row when this peer reconnects either way).
		delete(q.seen, itemKey(q.items[0]))
		q.items = q.items[1:]
	}
	q.items = append(q.items, item)
	q.seen[key] = struct{}{}
}

// loop is the batch ticker. Runs until Stop closes stopCh or ctx is
// cancelled. On exit it closes doneCh so Wait() unblocks.
func (c *Coordinator) loop(ctx context.Context) {
	defer close(c.doneCh)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.drain(ctx)
		}
	}
}

// drain pulls every per-peer queue's contents and dispatches them in
// parallel goroutines. Per-peer drains happen in one goroutine so
// ordering inside a peer's queue is preserved; cross-peer pushes are
// concurrent so a slow peer never blocks a fast one.
//
// Each Item dispatch uses a per-item context.WithTimeout so a stuck
// libp2p stream can't tie up the goroutine indefinitely. The drain
// loop returns AFTER spawning all per-peer dispatch goroutines — it
// does NOT wait for them; the next tick handles whatever didn't
// complete in the previous interval.
func (c *Coordinator) drain(ctx context.Context) {
	c.mu.Lock()
	snapshot := make(map[string][]Item, len(c.queues))
	for peerID, q := range c.queues {
		q.mu.Lock()
		if len(q.items) > 0 {
			snapshot[peerID] = q.items
			q.items = nil
			q.seen = make(map[string]struct{})
		}
		q.mu.Unlock()
	}
	c.mu.Unlock()
	if len(snapshot) == 0 {
		return
	}
	for peerID, items := range snapshot {
		go c.dispatchOnePeer(ctx, peerID, items)
	}
}

// dispatchOnePeer iterates over one peer's items, picking the right
// pusher for each Kind. Errors are logged at debug; the queue is
// already drained, so a failed item is genuinely dropped (the next
// write will re-enqueue, and a peer that's down for hours will pull
// missing rows via the existing on-demand share when it reconnects).
func (c *Coordinator) dispatchOnePeer(ctx context.Context, peerID string, items []Item) {
	// Snapshot taskPush once under c.mu — SetTaskReplication writes it
	// under the same lock, so a bare read here would race the wiring
	// call. memPush/skillPsh are set at construction and never mutated,
	// so they're safe to read without the lock.
	c.mu.Lock()
	taskPush := c.taskPush
	c.mu.Unlock()
	for _, it := range items {
		dispatchCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
		var err error
		switch it.Kind {
		case EventKindMemory:
			err = c.memPush.PushMemory(dispatchCtx, peerID, it.ID)
		case EventKindSkill:
			err = c.skillPsh.PushSkill(dispatchCtx, peerID, it.ID)
		case EventKindTask:
			if taskPush != nil {
				err = taskPush.PushTask(dispatchCtx, peerID, it.WorkspaceID, it.ID)
			}
		default:
			cancel()
			continue
		}
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Debug("replication: dispatch failed",
				"peer_id", peerID, "kind", it.Kind, "id", it.ID,
				"error", err)
		}
	}
}
