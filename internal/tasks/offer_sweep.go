// offer_sweep.go — TTL expiry for stale cross-peer task offers.
// Pending offers that nobody acted on rot in the inbox/outbox forever
// (production accumulated 100+); this sweep flips them to
// state='expired' with an audit stamp so listings stay actionable.
// Called from the daemon's 1-minute maintenance tick alongside
// SweepExpiredLeases (cmd/mcplexer/serve.go).
package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Offer TTLs. Outgoing offers stay visible longer — the sender may be
// waiting on a peer that's offline for days. Incoming offers mirror
// the wire-level staleness window (taskOfferStalenessWindow): an
// envelope older than 24h would be rejected on re-send anyway, so a
// pending incoming row past that age can never be meaningfully
// accepted fresh.
const (
	// OfferTTLOutgoing — pending outgoing offers expire after 7 days.
	OfferTTLOutgoing = 7 * 24 * time.Hour
	// OfferTTLIncoming — pending incoming offers expire after 24 hours.
	OfferTTLIncoming = 24 * time.Hour
)

// offerSweepBatchLimit caps rows per sweep pass; the 1-minute tick
// converges any backlog within a few passes.
const offerSweepBatchLimit = 500

// SweepExpiredOffers flips pending offers older than their direction's
// TTL to state='expired'. The audit stamp lands in declined_reason
// (the row's only freeform provenance column) so the dashboard + agents
// can see WHY the offer closed. Per-row failures are skipped, not
// fatal — the next tick retries. Returns the number of offers expired.
func (s *Service) SweepExpiredOffers(ctx context.Context) (int, error) {
	rows, err := s.store.ListTaskOffers(ctx, store.TaskOfferFilter{
		State: store.TaskOfferPending,
		Limit: offerSweepBatchLimit,
	})
	if err != nil {
		return 0, fmt.Errorf("sweep expired offers: %w", err)
	}
	now := time.Now().UTC()
	expired := 0
	for i := range rows {
		o := rows[i]
		ttl := OfferTTLIncoming
		if o.Direction == "outgoing" {
			ttl = OfferTTLOutgoing
		}
		age := now.Sub(o.CreatedAt)
		if age < ttl {
			continue
		}
		reason := fmt.Sprintf("expired by TTL sweep: pending %s offer for %dh (limit %dh)",
			o.Direction, int(age.Hours()), int(ttl.Hours()))
		if uerr := s.store.UpdateTaskOfferState(
			ctx, o.ID, store.TaskOfferExpired, nil, nil, reason, "", "",
		); uerr != nil {
			continue
		}
		o.State = store.TaskOfferExpired
		o.DeclinedReason = reason
		s.publishOfferEvent(&o)
		expired++
	}
	return expired, nil
}
