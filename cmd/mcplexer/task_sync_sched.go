// task_sync_sched.go — periodic catch-up scheduler for the
// /mcplexer/task-sync/1.0.0 gossip protocol.
//
// Pull model: every tick (and immediately when a paired peer comes back
// online via the reconnector hook) the scheduler walks the LINKED
// workspace bindings (workspace_peer_bindings rows with linked=1) and
// dials each linked peer with a Hello carrying the local HLC watermark
// per remote workspace. Un-linked workspaces are NEVER requested —
// linking is the operator's explicit opt-in; the serving peer
// additionally enforces the task_sync:<workspace> scope server-side.
package main

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	// taskSyncInterval is the steady-state catch-up cadence.
	taskSyncInterval = 60 * time.Second
	// taskSyncJitterMax de-synchronizes two daemons that booted
	// together so their pulls don't thundering-herd each other.
	taskSyncJitterMax = 10 * time.Second
	// taskSyncPeerBudget bounds one peer's catch-up dial so a hung
	// stream can't stall the whole cycle.
	taskSyncPeerBudget = 45 * time.Second
)

// taskSyncDialer is the slice of p2p.TaskSyncService the scheduler
// needs. Narrowed to an interface so tests inject a fake peer.
type taskSyncDialer interface {
	ConnectToPeer(ctx context.Context, peerID string, workspaces []p2p.TaskSyncHelloWorkspace) error
}

// taskSyncLinkLister is the slice of the store that enumerates linked
// workspace bindings (linked=1 rows only — see migration 088).
type taskSyncLinkLister interface {
	ListWorkspaceLinks(ctx context.Context) ([]store.WorkspacePeerBinding, error)
}

// taskSyncWatermarks resolves the local per-workspace HLC high-water
// mark sent in the Hello. Synced rows land under the REMOTE workspace
// id (gossip v1 has no cross-workspace remapping), so the watermark is
// keyed by the binding's remote_workspace_id.
type taskSyncWatermarks interface {
	MaxHLCForWorkspace(ctx context.Context, workspaceID string) (string, error)
}

// taskSyncScheduler drives periodic + on-reconnect catch-up pulls.
type taskSyncScheduler struct {
	dialer     taskSyncDialer
	links      taskSyncLinkLister
	watermarks taskSyncWatermarks
	logger     *slog.Logger
	interval   time.Duration
	jitterMax  time.Duration
	peerBudget time.Duration
}

// newTaskSyncScheduler builds a scheduler with production cadence.
// Returns nil when any dependency is missing so callers can wire
// unconditionally.
func newTaskSyncScheduler(
	dialer taskSyncDialer,
	links taskSyncLinkLister,
	watermarks taskSyncWatermarks,
	logger *slog.Logger,
) *taskSyncScheduler {
	if dialer == nil || links == nil || watermarks == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &taskSyncScheduler{
		dialer:     dialer,
		links:      links,
		watermarks: watermarks,
		logger:     logger,
		interval:   taskSyncInterval,
		jitterMax:  taskSyncJitterMax,
		peerBudget: taskSyncPeerBudget,
	}
}

// Start launches the periodic loop. Respects ctx for shutdown. The
// first cycle runs after one jitter delay (not immediately) so daemon
// boot isn't serialized behind potentially-offline peers.
func (s *taskSyncScheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.jitter()):
		}
		for {
			s.syncAll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.interval + s.jitter()):
			}
		}
	}()
}

// jitter returns a random delay in [0, jitterMax).
func (s *taskSyncScheduler) jitter() time.Duration {
	if s.jitterMax <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(s.jitterMax)))
}

// syncAll runs one catch-up cycle across every linked peer.
func (s *taskSyncScheduler) syncAll(ctx context.Context) {
	byPeer, err := s.linkedWorkspacesByPeer(ctx, "")
	if err != nil {
		s.logger.Debug("task-sync: list links", "err", err)
		return
	}
	for peerID, workspaces := range byPeer {
		if ctx.Err() != nil {
			return
		}
		s.syncPeer(ctx, peerID, workspaces)
	}
}

// SyncPeerNow runs an immediate catch-up against one peer — wired into
// the reconnector's online-transition observer so a peer coming back
// from a partition is reconciled without waiting for the next tick.
// Safe to call from any goroutine; no-op when the peer has no links.
func (s *taskSyncScheduler) SyncPeerNow(peerID string) {
	if s == nil || peerID == "" {
		return
	}
	ctx := context.Background()
	byPeer, err := s.linkedWorkspacesByPeer(ctx, peerID)
	if err != nil {
		s.logger.Debug("task-sync: list links for peer", "peer", peerID, "err", err)
		return
	}
	if workspaces := byPeer[peerID]; len(workspaces) > 0 {
		s.syncPeer(ctx, peerID, workspaces)
	}
}

// linkedWorkspacesByPeer groups the linked bindings into per-peer Hello
// workspace lists, each entry carrying the local watermark. onlyPeer
// filters to a single peer when non-empty.
func (s *taskSyncScheduler) linkedWorkspacesByPeer(
	ctx context.Context, onlyPeer string,
) (map[string][]p2p.TaskSyncHelloWorkspace, error) {
	links, err := s.links.ListWorkspaceLinks(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]p2p.TaskSyncHelloWorkspace)
	for _, l := range links {
		if l.PeerID == "" || l.RemoteWorkspaceID == "" {
			continue
		}
		if onlyPeer != "" && l.PeerID != onlyPeer {
			continue
		}
		since, err := s.watermarks.MaxHLCForWorkspace(ctx, l.RemoteWorkspaceID)
		if err != nil {
			// No rows yet / transient store error — ask for everything.
			since = ""
		}
		out[l.PeerID] = append(out[l.PeerID], p2p.TaskSyncHelloWorkspace{
			WorkspaceID: l.RemoteWorkspaceID,
			SinceHLC:    since,
		})
	}
	return out, nil
}

// syncPeer dials one peer with its workspace list, chunked under the
// protocol's per-Hello cap, each chunk bounded by peerBudget. Failures
// are routine (peer offline) — logged at debug, retried next tick.
func (s *taskSyncScheduler) syncPeer(
	ctx context.Context, peerID string, workspaces []p2p.TaskSyncHelloWorkspace,
) {
	for start := 0; start < len(workspaces); start += p2p.MaxTaskSyncWorkspacesPerHello {
		end := start + p2p.MaxTaskSyncWorkspacesPerHello
		if end > len(workspaces) {
			end = len(workspaces)
		}
		dialCtx, cancel := context.WithTimeout(ctx, s.peerBudget)
		err := s.dialer.ConnectToPeer(dialCtx, peerID, workspaces[start:end])
		cancel()
		if err != nil {
			s.logger.Debug("task-sync: catch-up dial failed",
				"peer", peerID, "workspaces", end-start, "err", err)
			return
		}
	}
}
