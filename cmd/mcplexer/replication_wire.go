// replication_wire.go — daemon-side glue for the Tier-1 (same-user)
// silent replication coordinator. Lives in cmd/mcplexer so the
// internal/replication package stays free of store + p2p imports.
//
// Wiring shape:
//
//	memory.Service.Notify ─┐
//	                       ├──► chained adapter ──► replication.Coordinator
//	(existing notifyBus)  ─┘                          │
//	                                                  ▼
//	                              MemoryPusher (uses p2p.MemoryShareService)
//	                              SkillPusher  (uses p2p.SkillShareService)
//
// The chained adapter preserves the existing notify.Bus fan-out (so
// the dashboard's /memory page keeps lighting up live) AND adds the
// replication hook in front. Order: replicator first (queueing is
// effectively free), then notify so a slow SSE consumer can't delay
// the queue.
//
// Skill installs flow through OnSkillInstall called explicitly from:
//   - cmd/mcplexer/skill_install.go   (CLI install)
//   - internal/api/...skill_install.go (REST install, if any)
//
// Receive-side installs from skill_share's HandleIncomingBundle MUST
// pass peerOriginInstall=true so the coordinator drops the event and
// prevents echo storms.
package main

import (
	"context"
	"log/slog"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/replication"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// buildReplicationCoordinator wires the coordinator + returns it
// ready-to-Start. Returns nil when any dependency is missing — the
// daemon then logs "auto-replication disabled" and life continues.
//
// Pre-conditions for a live coordinator:
//   - p2pHost present (libp2p enabled + ready)
//   - memoryShare service present (always non-nil in stub mode too,
//     but PushMemory is a no-op without a real p2p stack)
//   - skillShare service present (same)
//   - consent.Resolver wired
//
// The slim build returns a coordinator that runs the bookkeeping
// (queues, ticker, opt-out check) but whose pushers ultimately call
// p2p.MemoryShareService.OfferMemory / RequestMemory which
// short-circuit to ErrP2PNotBuiltIn. The end-to-end effect is
// "auto-replication is a no-op in slim builds" — the queue drains,
// the push fails, the next write retries; no panics, no leaks.
func buildReplicationCoordinator(
	host *p2p.Host,
	memShare *p2p.MemoryShareService,
	skillShare *p2p.SkillShareService,
	memSvc *memory.Service,
	peerStore store.P2PPeerStore,
	resolver consent.Resolver,
	skillsDir string,
) *replication.Coordinator {
	if host == nil || memShare == nil || skillShare == nil {
		slog.Info("replication: auto-rep disabled (p2p unavailable)")
		return nil
	}
	if resolver == nil {
		slog.Info("replication: auto-rep disabled (no consent resolver)")
		return nil
	}
	if peerStore == nil {
		return nil
	}
	memPusher := &memoryReplicationPusher{
		share: memShare,
		mem:   memSvc,
	}
	skillPusher := &skillReplicationPusher{
		share:     skillShare,
		skillsDir: skillsDir,
	}
	peers := &peerStoreAdapter{store: peerStore}
	cfg := replication.Config{
		Logger: slog.Default(),
	}
	c := replication.NewCoordinator(
		tierResolverAdapter{r: resolver},
		peers,
		memPusher,
		skillPusher,
		cfg,
	)
	if c != nil {
		slog.Info("replication: auto-rep ready",
			"batch_interval", c.Interval(),
			"source", replication.IntervalSource())
	}
	return c
}

// tierResolverAdapter narrows the daemon's consent.Resolver to the
// shape replication.TierResolver wants (just TierFor, no auto-pair or
// grant-origin). Keeps the replication package free of a consent
// dependency on the full Resolver interface.
type tierResolverAdapter struct{ r consent.Resolver }

func (a tierResolverAdapter) TierFor(ctx context.Context, peerID string) consent.Tier {
	if a.r == nil {
		return consent.TierCrossOrg
	}
	return a.r.TierFor(ctx, peerID)
}

// peerStoreAdapter converts store.P2PPeerStore.ListPeers (a wide
// model) into the slim replication.PeerInfo shape. Skips revoked
// rows so a revoked peer's queue can't accumulate.
type peerStoreAdapter struct{ store store.P2PPeerStore }

func (a *peerStoreAdapter) ListActivePairedPeers(ctx context.Context) ([]replication.PeerInfo, error) {
	rows, err := a.store.ListPeers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]replication.PeerInfo, 0, len(rows))
	for _, r := range rows {
		if r.RevokedAt != nil {
			continue
		}
		out = append(out, replication.PeerInfo{
			PeerID: r.PeerID,
			Scopes: append([]string(nil), r.Scopes...),
		})
	}
	return out, nil
}

// memoryReplicationPusher implements replication.MemoryPusher by
// invoking the existing memory-share offer-then-request flow. Tier-1
// pairs auto-grant mesh.memory_request on each other at pair-time, so
// the receiver-side scope check passes silently.
//
// For now we use OfferMemory (the offer-then-request pattern): a peer
// that gets the offer will silently auto-Request behind the scenes if
// auto-pull is wired, OR the user sees an "incoming offer" tile and
// approves. Future revision: add a direct "PushMemoryDirect" wire op
// on MemoryShareService that bypasses the offer step for Tier-1 pairs.
// Until then, offer-then-pull is the safe wire posture (the receiver
// always has the final say on what lands in their store).
type memoryReplicationPusher struct {
	share *p2p.MemoryShareService
	mem   *memory.Service
}

func (p *memoryReplicationPusher) PushMemory(
	ctx context.Context, peerID, memoryID string,
) error {
	if p == nil || p.share == nil || p.mem == nil {
		return nil
	}
	entry, err := p.mem.Get(ctx, memoryID)
	if err != nil {
		// Memory may have been forgotten in the window between the
		// write and this dispatch. Drop silently.
		return nil
	}
	offer := &p2p.MemoryOffer{
		RemoteID:  entry.ID,
		Name:      entry.Name,
		Kind:      entry.Kind,
		SizeBytes: int64(len(entry.Content)),
	}
	if len(entry.Content) > 0 {
		preview := entry.Content
		if len(preview) > 512 {
			preview = preview[:512]
		}
		offer.Preview = preview
	}
	return p.share.OfferMemory(ctx, peerID, offer)
}

// skillReplicationPusher implements replication.SkillPusher by
// invoking the existing skill-share offer flow. The receiver side
// runs the standard verify-+-install pipeline on whatever bundle the
// peer eventually pulls.
type skillReplicationPusher struct {
	share     *p2p.SkillShareService
	skillsDir string
}

func (p *skillReplicationPusher) PushSkill(
	ctx context.Context, peerID, skillName string,
) error {
	if p == nil || p.share == nil {
		return nil
	}
	return p.share.OfferSkill(ctx, peerID, skillName)
}

// taskReplicationPusher implements replication.TaskPusher by invoking
// the existing silent direct-assign path (tasks.Service.AssignRemote) —
// the same wire op task__assign_remote uses. The receiver materializes
// (and, once receive-side convergence lands, updates) the task in its
// linked workspace. Tier-1 same-user linked pairs carry the task_assign
// scope so the receiver-side check passes; a missing scope / throttle
// rejection surfaces as an error that the coordinator logs at debug and
// retries on the next mutation.
type taskReplicationPusher struct {
	tasks *tasks.Service
}

func (p *taskReplicationPusher) PushTask(
	ctx context.Context, peerID, workspaceID, taskID string,
) error {
	if p == nil || p.tasks == nil {
		return nil
	}
	_, err := p.tasks.AssignRemote(ctx, tasks.AssignRemoteOptions{
		WorkspaceID: workspaceID,
		TaskID:      taskID,
		ToPeerID:    peerID,
	})
	return err
}

// linkListerAdapter narrows the store to the single method
// replication.LinkLister needs, keeping the replication package free of
// a store dependency.
type linkListerAdapter struct {
	store interface {
		ListLinkedPeersForWorkspace(ctx context.Context, localWorkspaceID string) ([]string, error)
	}
}

func (a linkListerAdapter) LinkedPeersForWorkspace(
	ctx context.Context, localWorkspaceID string,
) ([]string, error) {
	if a.store == nil {
		return nil, nil
	}
	return a.store.ListLinkedPeersForWorkspace(ctx, localWorkspaceID)
}

// chainedMemoryNotify wraps two memory.Service.Notify functions so the
// existing notifyBus path keeps firing AND the replicator gets the
// same event. Used in serve.go after buildReplicationCoordinator.
//
// The replicator's hook runs FIRST so a slow SSE consumer can't delay
// queue insertion (queueing is sub-microsecond; SSE publish hits a
// channel send which could block under backpressure).
func chainedMemoryNotify(
	rep *replication.Coordinator, downstream func(context.Context, memory.Event),
) func(context.Context, memory.Event) {
	if rep == nil && downstream == nil {
		return nil
	}
	return func(ctx context.Context, ev memory.Event) {
		if rep != nil {
			rep.OnMemoryEvent(ctx, ev.Kind, ev.MemoryID, ev.Source)
		}
		if downstream != nil {
			downstream(ctx, ev)
		}
	}
}
