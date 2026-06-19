package replication

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/consent"
)

// DefaultBatchInterval is the per-peer drain cadence. 5s matches the
// "your phone just works" UX promise — fast enough that a write on
// the laptop appears on the desktop while the user is still looking
// at it, slow enough to amortize libp2p stream overhead across
// multiple writes in a burst.
const DefaultBatchInterval = 5 * time.Second

// ReplicationBatchIntervalEnv overrides DefaultBatchInterval. Format:
// any Go duration string ("2s", "500ms"). Invalid values fall back to
// the default — the daemon logs a warning at start-up.
const ReplicationBatchIntervalEnv = "MCPLEXER_REPLICATION_BATCH_INTERVAL"

// ReplicationOptOutScope is the opt-out marker. Presence in a paired
// peer's scopes disables auto-replication for that peer. Absence ==
// auto-replicate (silent default for Tier 1 same-user).
//
// The string is namespaced under mesh.* so it surfaces in the same
// dashboard "granted scopes" UI as every other peer scope. Granting
// it via mesh__grant_peer_scope is the standard opt-out gesture.
const ReplicationOptOutScope = "mesh.auto_replicate_off"

// kindMemoryWrite is the only memory.Event.Kind we replicate today.
// invalidate/delete/link/pin are observability events on the receiver
// side too — they don't need to fan out (the next write replays the
// state). Keeping the surface narrow simplifies the audit story.
const kindMemoryWrite = "write"

// EventKind discriminates the queue item type. Keep the wire form
// stable: callers may switch on it.
type EventKind string

const (
	EventKindMemory EventKind = "memory"
	EventKindSkill  EventKind = "skill"
	EventKindTask   EventKind = "task"
)

// Item is one queued replication payload. Kept tiny — payloads
// dereference local store rows at dispatch time so a queued backlog
// doesn't hold large memory content in RAM.
type Item struct {
	Kind EventKind
	// ID is the local memory id (for EventKindMemory), skill name (for
	// EventKindSkill), or task id (for EventKindTask). The pusher
	// resolves the full bytes at dispatch time.
	ID string
	// WorkspaceID is set for EventKindTask only — the local workspace the
	// task lives in, which the TaskPusher needs to build the outbound
	// offer envelope and which the dedup key folds in so two tasks with
	// the same id across workspaces (impossible today, but cheap to be
	// safe) never collide.
	WorkspaceID string
}

// MemoryPusher dispatches one queued memory to one peer. The
// implementation opens a libp2p stream, fetches the memory from the
// local store, and OFFERs (or directly pushes) the payload. Tier-1
// pairs always have mesh.memory_request scope on each other (auto-
// granted at pair-time), so an immediate Request is also valid.
//
// The pusher returns an error only when the dispatch failed in a way
// the coordinator should log — peer disconnection is normal and
// should be returned silently as nil (the coordinator retries on the
// next tick via the queued state).
type MemoryPusher interface {
	PushMemory(ctx context.Context, peerID, memoryID string) error
}

// SkillPusher is the equivalent for skill bundles. The implementation
// resolves the on-disk bundle bytes via skills.ReadBundleCache and
// OFFERs the skill to the peer.
type SkillPusher interface {
	PushSkill(ctx context.Context, peerID, skillName string) error
}

// TaskPusher dispatches one queued task to one linked peer. The daemon
// implementation resolves the task by id and calls
// tasks.Service.AssignRemote — the same silent direct-assign path the
// task__assign_remote tool uses — so the peer materializes (or, once the
// receive-side converges, updates) the task in its linked workspace.
//
// Like MemoryPusher, peer disconnection is normal: return nil silently
// so the coordinator retries on the next tick via the re-enqueued write.
type TaskPusher interface {
	PushTask(ctx context.Context, peerID, workspaceID, taskID string) error
}

// LinkLister reports which peers a local workspace is linked to. This is
// the send-side gate that makes task replication link-scoped rather than
// fanning to every Tier-1 peer like memory/skills do. Backed by
// store.ListLinkedPeersForWorkspace; declared locally so the package
// stays free of a store dependency.
type LinkLister interface {
	LinkedPeersForWorkspace(ctx context.Context, localWorkspaceID string) ([]string, error)
}

// TierResolver is the consent.Resolver subset the coordinator needs.
// Declared locally so a fake in tests doesn't pull a heavy resolver
// dependency.
type TierResolver interface {
	TierFor(ctx context.Context, peerID string) consent.Tier
}

// PeerLister returns the active paired-peer list. The coordinator
// pulls (peer_id, scopes) and ignores revoked rows.
type PeerLister interface {
	ListActivePairedPeers(ctx context.Context) ([]PeerInfo, error)
}

// PeerInfo is the slim view the coordinator needs about a paired peer.
// Defined locally so we don't drag store.P2PPeer (and an indirect
// time.Time dependency tree) into the package surface.
type PeerInfo struct {
	PeerID string
	Scopes []string
}

// HasScope reports whether the named scope is granted on this peer.
// Case-sensitive, exact match (matches the existing hasScope helper
// in internal/p2p).
func (p PeerInfo) HasScope(name string) bool {
	for _, s := range p.Scopes {
		if s == name {
			return true
		}
	}
	return false
}

// Config tunes the coordinator. Zero values map to sane defaults so
// callers can construct one with just the four dependencies.
type Config struct {
	// BatchInterval overrides DefaultBatchInterval. Zero or negative
	// means "use the default". Env override is applied on top of this
	// during NewCoordinator so the operator always wins.
	BatchInterval time.Duration
	// Logger is the slog.Logger to use. nil = slog.Default().
	Logger *slog.Logger
}
