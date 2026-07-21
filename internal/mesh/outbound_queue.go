package mesh

// Offline-delivery queue for the cross-machine p2p mesh.
//
// Lifecycle walk-through ("Alice sends to Bob who's offline"):
//
//  1. Alice's agent calls mesh__send with to_peer="bob". Manager.Send
//     persists the message to mesh_messages locally, then calls
//     dispatchP2P. The libp2p stream open to Bob fails (peer offline /
//     unreachable / NAT-flap).
//  2. dispatchP2P calls OutboundQueue.Enqueue instead of returning an
//     error. The envelope is JSON-encoded into mesh_outbound_queue,
//     keyed by the message_id ULID for dedup. A "system" notify Event
//     fires so the Signal tray shows "Queued for offline peer X".
//  3. Bob's daemon comes back online; the libp2p Reconnector observes
//     the dial succeed and calls Reconnector.SetOnlineObserver →
//     OutboundQueue.DrainForPeer(bob). Each due row is replayed via
//     the same dispatch path. On success delivered_at = now; on
//     failure attempts++, last_error set, next_attempt_at backed off
//     exponentially (capped at 5min).
//  4. A separate goroutine sweeps every 30s and retries any
//     next_attempt_at <= now row whose target peer libp2p believes is
//     connected — belt-and-braces for missed reconnect events.
//  5. A daily prune deletes delivered rows > 1 day old and any
//     undelivered rows that have aged past expires_at (default 7d).
//     Expired-undelivered rows get a warn-level log first.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// Queue tuning. All values are intentionally generous — the queue's job is
// to outlive a peer being offline for a long weekend, not to retry
// aggressively. The peer-online-transition signal is the happy path; these
// schedules just bound worst-case behaviour.
const (
	// outboundDefaultTTL is the hard expiry stamped on enqueued rows.
	// 7 days covers most "laptop closed over the weekend" scenarios.
	outboundDefaultTTL = 7 * 24 * time.Hour
	// outboundMinBackoff is the floor on next_attempt_at delay.
	outboundMinBackoff = 30 * time.Second
	// outboundMaxBackoff caps the exponential schedule at 5 minutes.
	outboundMaxBackoff = 5 * time.Minute
	// outboundDeliveredRetention keeps delivered rows around for 1 day
	// so admin tooling can see "this just shipped". Then they're pruned.
	outboundDeliveredRetention = 24 * time.Hour
	// outboundSweepInterval is the background "retry anything due"
	// cadence — backstop for missed online-transition events.
	outboundSweepInterval = 30 * time.Second
	// outboundPruneInterval is the daily prune cadence.
	outboundPruneInterval = 24 * time.Hour
	// outboundDrainBatchSize caps the rows touched in one drain pass.
	// Bounds the worst-case "10k queued messages, peer just came back".
	outboundDrainBatchSize = 64
	// outboundDispatchTimeout caps a single SendToPeer attempt during
	// drain. SendToPeer itself uses a 10s dial timeout — this is the
	// outer ceiling.
	outboundDispatchTimeout = 15 * time.Second
)

// peerOnlineChecker is the narrow read OutboundQueue uses during the 30s
// sweep to decide whether to retry now or wait. Implemented in production
// by *p2p.LivenessMonitor; nil-safe (returns false from IsOnline).
type peerOnlineChecker interface {
	IsOnline(peerID string) bool
}

// outboundStore narrows store.MeshStore to the methods the queue needs.
// Lets tests pass an in-memory fake without implementing the whole MeshStore.
type outboundStore interface {
	EnqueueMeshOutbound(ctx context.Context, o *store.MeshOutbound) error
	ListDueMeshOutbound(ctx context.Context, peerID string, now time.Time, limit int) ([]store.MeshOutbound, error)
	ListPendingMeshOutbound(ctx context.Context, now time.Time, limit int) ([]store.MeshOutbound, error)
	ListExpiredMeshOutbound(ctx context.Context, now time.Time, limit int) ([]store.MeshOutbound, error)
	MarkMeshOutboundDelivered(ctx context.Context, messageID string, now time.Time) error
	BumpMeshOutboundAttempt(ctx context.Context, messageID, lastErr string, nextAttemptAt time.Time) error
	PruneMeshOutbound(ctx context.Context, deliveredBefore, expiredBefore time.Time) (int, error)
}

// outboundSender is the narrow write the queue uses to replay an envelope.
// Matches the p2p transport's SendToPeer; injectable for tests.
type outboundSender interface {
	SendToPeer(ctx context.Context, peerID string, env *p2p.MeshEnvelope) error
}

// queuedEnvelope is the on-disk wire format. We persist a MeshEnvelope plus
// the routing hint (target session id) so a future protocol upgrade can add
// fields here without changing the queue table schema. JSON keeps the
// at-rest data legible during debugging.
type queuedEnvelope struct {
	Envelope             p2p.MeshEnvelope `json:"envelope"`
	TargetAgentSessionID string           `json:"target_agent_session_id,omitempty"`
}

// OutboundQueue owns the queue lifecycle: enqueue on dispatch failure, drain
// on peer-online transition, periodic sweep + prune. Construct via
// NewOutboundQueue and Start once per process.
type OutboundQueue struct {
	store    outboundStore
	sender   outboundSender
	liveness peerOnlineChecker
	notify   *notify.Bus
	logger   *slog.Logger
	clk      func() time.Time

	// drainMu serialises drain runs per peer so a sweep + a reconnect
	// don't double-deliver. We accept a brief queue (only one drain
	// in flight per peer at a time) — cheap because drains are short.
	drainMu sync.Mutex
	drainIn map[string]bool
}

// NewOutboundQueue wires the queue. store + sender + logger must be non-nil;
// liveness + notifyBus are optional.
func NewOutboundQueue(
	s outboundStore, sender outboundSender,
	liveness peerOnlineChecker, notifyBus *notify.Bus, logger *slog.Logger,
) *OutboundQueue {
	if logger == nil {
		logger = slog.Default()
	}
	return &OutboundQueue{
		store:    s,
		sender:   sender,
		liveness: liveness,
		notify:   notifyBus,
		logger:   logger,
		clk:      func() time.Time { return time.Now().UTC() },
		drainIn:  make(map[string]bool),
	}
}

// Enqueue parks an envelope for a peer that's currently unreachable. The
// caller is responsible for having already confirmed the dispatch failed —
// Enqueue does not retry inline. Returns an error only when the underlying
// store call fails; an already-queued message_id is a no-op (nil error).
func (q *OutboundQueue) Enqueue(
	ctx context.Context, targetPeerID, targetAgentSessionID string,
	env *p2p.MeshEnvelope, dispatchErr error,
) error {
	if q == nil || q.store == nil {
		return errors.New("outbound queue: not configured")
	}
	if env == nil || env.ID == "" || targetPeerID == "" {
		return errors.New("outbound queue: envelope.ID and target_peer_id required")
	}
	wire, err := json.Marshal(queuedEnvelope{
		Envelope:             *env,
		TargetAgentSessionID: targetAgentSessionID,
	})
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}
	now := q.clk()
	errStr := ""
	if dispatchErr != nil {
		errStr = dispatchErr.Error()
	}
	row := &store.MeshOutbound{
		MessageID:            env.ID,
		TargetPeerID:         targetPeerID,
		TargetAgentSessionID: targetAgentSessionID,
		Envelope:             wire,
		Attempts:             1,
		LastError:            errStr,
		EnqueuedAt:           now,
		NextAttemptAt:        now.Add(outboundMinBackoff),
		ExpiresAt:            now.Add(outboundDefaultTTL),
	}
	if err := q.store.EnqueueMeshOutbound(ctx, row); err != nil {
		return err
	}
	q.publishEnqueueNotice(env, targetPeerID)
	q.logger.Info("mesh outbound: queued for offline peer",
		"peer", targetPeerID, "id", env.ID,
		"kind", env.Kind, "err", errStr)
	return nil
}

// publishEnqueueNotice fires a low-priority "system"-source notification
// so the Signal tray surfaces "Queued for offline peer X" without the
// caller having to wire one in. Best-effort, nil-safe.
func (q *OutboundQueue) publishEnqueueNotice(env *p2p.MeshEnvelope, peerID string) {
	if q.notify == nil {
		return
	}
	q.notify.Publish(notify.Event{
		MessageID: "mesh-outbound:" + env.ID,
		Source:    "system",
		Kind:      "mesh_outbound_queued",
		Priority:  "low",
		Title:     "Message queued for offline peer",
		Body: fmt.Sprintf(
			"Peer %s is unreachable; %s message will deliver when they come back online.",
			shortPeer(peerID), env.Kind),
		Tags:      "mesh,outbound,offline",
		Link:      "/mesh?queue=1",
		CreatedAt: q.clk(),
	})
}

// shortPeer returns a log-friendly tail of a libp2p peer ID.
func shortPeer(p string) string {
	if len(p) <= 12 {
		return p
	}
	return p[:6] + "…" + p[len(p)-6:]
}
