package mesh

// Drain + sweep + prune for the offline-delivery queue. See
// outbound_queue.go for the lifecycle overview.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// Start launches the background sweeper + daily prune. Returns immediately;
// goroutines exit on ctx.Done().
func (q *OutboundQueue) Start(ctx context.Context) {
	if q == nil {
		return
	}
	go q.sweepLoop(ctx)
	go q.pruneLoop(ctx)
}

// DrainForPeer ships every due, unexpired row targeting peerID. Idempotent
// and safe to call from multiple producers (reconnect-observer + sweeper);
// per-peer drain serialisation prevents double-delivery.
func (q *OutboundQueue) DrainForPeer(ctx context.Context, peerID string) {
	if q == nil || q.store == nil || q.sender == nil || peerID == "" {
		return
	}
	if !q.beginDrain(peerID) {
		return // another drain is already in flight for this peer
	}
	defer q.endDrain(peerID)

	now := q.clk()
	rows, err := q.store.ListDueMeshOutbound(ctx, peerID, now, outboundDrainBatchSize)
	if err != nil {
		q.logger.Warn("mesh outbound: drain query failed",
			"peer", peerID, "err", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		q.deliverOne(ctx, row)
	}
}

// beginDrain marks the peer as in-flight; returns false if a drain is
// already running for the peer (in which case the caller should bail).
func (q *OutboundQueue) beginDrain(peerID string) bool {
	q.drainMu.Lock()
	defer q.drainMu.Unlock()
	if q.drainIn == nil {
		q.drainIn = make(map[string]bool)
	}
	if q.drainIn[peerID] {
		return false
	}
	q.drainIn[peerID] = true
	return true
}

func (q *OutboundQueue) endDrain(peerID string) {
	q.drainMu.Lock()
	defer q.drainMu.Unlock()
	delete(q.drainIn, peerID)
}

// deliverOne replays a single queued envelope. On success the row is
// stamped delivered_at; on failure attempts++ and next_attempt_at is
// pushed out by exponentialBackoff(attempts).
func (q *OutboundQueue) deliverOne(ctx context.Context, row store.MeshOutbound) {
	env, err := decodeQueuedEnvelope(row.Envelope)
	if err != nil {
		// Malformed row — push delivery far into the future so the same
		// poison-pill doesn't burn CPU every sweep. The row will age out
		// via expires_at.
		q.logger.Warn("mesh outbound: decode envelope failed; deferring",
			"id", row.MessageID, "err", err)
		_ = q.store.BumpMeshOutboundAttempt(ctx, row.MessageID,
			"decode: "+err.Error(),
			q.clk().Add(outboundMaxBackoff))
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, outboundDispatchTimeout)
	defer cancel()
	if err := q.sender.SendToPeer(sendCtx, row.TargetPeerID, env); err != nil {
		next := q.clk().Add(exponentialBackoff(row.Attempts + 1))
		_ = q.store.BumpMeshOutboundAttempt(ctx, row.MessageID, err.Error(), next)
		q.logger.Debug("mesh outbound: retry failed",
			"peer", row.TargetPeerID, "id", row.MessageID,
			"attempts", row.Attempts+1, "err", err, "next", next)
		return
	}
	if err := q.store.MarkMeshOutboundDelivered(ctx, row.MessageID, q.clk()); err != nil {
		q.logger.Warn("mesh outbound: mark delivered failed (delivery succeeded)",
			"id", row.MessageID, "err", err)
		return
	}
	q.logger.Info("mesh outbound: delivered from queue",
		"peer", row.TargetPeerID, "id", row.MessageID,
		"attempts", row.Attempts+1,
		"queued_for", q.clk().Sub(row.EnqueuedAt))
}

// sweepLoop runs every outboundSweepInterval and re-tries any row whose
// next_attempt_at has elapsed AND whose target peer is currently believed
// online. Belt-and-braces for missed reconnect observations.
func (q *OutboundQueue) sweepLoop(ctx context.Context) {
	t := time.NewTicker(outboundSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			q.runSweep(ctx)
		}
	}
}

// runSweep fans queued rows by target peer + delegates each to DrainForPeer
// when the peer is currently online. Offline peers are skipped — they'll
// drain on the reconnect signal instead, saving us from hammering the
// libp2p dial path for known-down peers.
func (q *OutboundQueue) runSweep(ctx context.Context) {
	if q == nil || q.store == nil {
		return
	}
	now := q.clk()
	rows, err := q.store.ListPendingMeshOutbound(ctx, now, listMeshOutboundCap())
	if err != nil {
		q.logger.Debug("mesh outbound: sweep query failed", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if _, ok := seen[row.TargetPeerID]; ok {
			continue
		}
		seen[row.TargetPeerID] = struct{}{}
		if row.NextAttemptAt.After(now) {
			continue
		}
		if !q.peerOnline(row.TargetPeerID) {
			continue
		}
		q.DrainForPeer(ctx, row.TargetPeerID)
	}
}

// peerOnline consults the liveness oracle. Conservative when no oracle is
// wired — we return true so the sweep still attempts delivery (the dial
// itself will tell us the truth at the cost of one libp2p RTT).
func (q *OutboundQueue) peerOnline(peerID string) bool {
	if q.liveness == nil {
		return true
	}
	return q.liveness.IsOnline(peerID)
}

// pruneLoop runs the daily expiry sweep + delivered-row cleanup.
func (q *OutboundQueue) pruneLoop(ctx context.Context) {
	t := time.NewTicker(outboundPruneInterval)
	defer t.Stop()
	// One immediate pass on startup so a daemon that crashed mid-day
	// doesn't carry stale rows around for 24 more hours.
	q.runPrune(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			q.runPrune(ctx)
		}
	}
}

// runPrune logs every expired-undelivered row as a warn then deletes both
// expired rows and delivered rows older than outboundDeliveredRetention.
func (q *OutboundQueue) runPrune(ctx context.Context) {
	if q == nil || q.store == nil {
		return
	}
	now := q.clk()
	expired, err := q.store.ListExpiredMeshOutbound(ctx, now, listMeshOutboundCap())
	if err != nil {
		q.logger.Debug("mesh outbound: prune-list failed", "err", err)
	} else {
		for _, row := range expired {
			q.logger.Warn("mesh outbound: dropping expired undelivered message",
				"peer", row.TargetPeerID, "id", row.MessageID,
				"queued_for", now.Sub(row.EnqueuedAt),
				"attempts", row.Attempts, "last_err", row.LastError)
		}
	}
	deliveredBefore := now.Add(-outboundDeliveredRetention)
	n, err := q.store.PruneMeshOutbound(ctx, deliveredBefore, now)
	if err != nil {
		q.logger.Debug("mesh outbound: prune failed", "err", err)
		return
	}
	if n > 0 {
		q.logger.Debug("mesh outbound: pruned", "rows", n)
	}
}

// ListPending exposes the queue contents for admin tooling
// (mesh__list_queue). Wraps the store call so callers don't need the
// store interface directly.
func (q *OutboundQueue) ListPending(ctx context.Context) ([]store.MeshOutbound, error) {
	if q == nil || q.store == nil {
		return nil, nil
	}
	return q.store.ListPendingMeshOutbound(ctx, q.clk(), listMeshOutboundCap())
}

// decodeQueuedEnvelope reverses Enqueue's JSON encoding.
func decodeQueuedEnvelope(blob []byte) (*p2p.MeshEnvelope, error) {
	if len(blob) == 0 {
		return nil, errors.New("empty envelope blob")
	}
	var qe queuedEnvelope
	if err := json.Unmarshal(blob, &qe); err != nil {
		return nil, err
	}
	env := qe.Envelope
	return &env, nil
}

// exponentialBackoff produces a ramp 30s → 1m → 2m → 4m → 5m capped.
func exponentialBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := outboundMinBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= outboundMaxBackoff {
			return outboundMaxBackoff
		}
	}
	return d
}

// listMeshOutboundCap is a wrapper so tests can override the cap without
// touching the package-level constant.
var listMeshOutboundCapVar = 1000

func listMeshOutboundCap() int { return listMeshOutboundCapVar }
