package api

// monitoring_notify_outcome.go — the REST notify surface answering the question
// it was actually asked.
//
// `POST /monitoring/notify` reported `{"dispatched": true}` / 200 whenever the
// dispatcher returned a nil error. That is not the same question. Notify
// returns nil on several paths where nothing reached anyone — most sharply
// `reminderNotDelivered`, which retries a failing route for six sweep ticks and
// then deliberately releases it to policy backoff and returns nil. The backoff
// is correct; not hammering a dead route every five minutes is right. What was
// wrong is that a synchronous caller asking "did this send work?" was told YES
// about a route that had never once accepted a message.
//
// The rig caught it precisely: driving eight notifications through a webhook
// that 400s every time produced 500 on sends 1-6 and 8, and **200 on send 7** —
// the tick where the retry budget was released. That is the 2026-07-14
// mechanism generalised: a suppression decision, sound in isolation, rendering
// failure indistinguishable from success at the boundary where someone is
// actually asking.

import (
	"context"
	"net/http"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/logwatch/escalate"
)

// outcomeNotifier is the richer contract *escalate.Dispatcher satisfies.
// Optional and type-asserted: a notifier that only implements distill.Notifier
// keeps the old coarse behaviour rather than failing to wire.
type outcomeNotifier interface {
	NotifyWithOutcome(ctx context.Context, n distill.Notification) (escalate.Outcome, error)
}

// notifyResponse is the honest answer. `dispatched` is retained for existing
// callers but now means what its name always implied — at least one route
// accepted the message — rather than "the call returned without an error".
type notifyResponse struct {
	Dispatched bool     `json:"dispatched"`
	Status     string   `json:"status"`
	Delivered  int      `json:"delivered"`
	Attempted  int      `json:"attempted"`
	Failures   []string `json:"failures,omitempty"`
	Suppressed string   `json:"suppressed,omitempty"`
}

// notifyStatusCode maps a delivery outcome onto HTTP.
//
// The mapping is uniform on the outcome rather than on whether an error came
// back, because the same state reaching the caller under two different codes is
// how this got missed in the first place.
//
//   - delivered      → 200. Someone was told.
//   - not_attempted  → 200. No route was eligible: every channel sits above this
//     severity, or none is configured. Nothing was tried, so nothing was lost —
//     a legitimate no-op, and `told` is false so the caller can still see it.
//   - suppressed     → 200. The throttle deliberately withheld it. A truthful
//     answer to a caller who asked, and cheap to retry. NOT an error: making it
//     one would push callers to retry around the throttle, which is how spam
//     gets alerting muted, which is the original incident by another route.
//   - failed         → 502. A route was eligible, was tried, and refused. The
//     operator was not told and the cause is upstream of us.
func notifyStatusCode(status escalate.DeliveryStatus) int {
	if status == escalate.StatusFailed {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

// writeNotifyOutcome renders the outcome. Failures are route-level strings that
// never carry channel secrets (the dispatcher guarantees this), so they are
// safe to hand back to an API caller.
func writeNotifyOutcome(w http.ResponseWriter, outcome escalate.Outcome) {
	body := notifyResponse{
		Dispatched: outcome.Told(),
		Status:     string(outcome.Status),
		Delivered:  outcome.Delivered,
		Attempted:  outcome.Attempted,
		Failures:   outcome.Failures,
		Suppressed: outcome.Suppressed,
	}
	writeJSON(w, notifyStatusCode(outcome.Status), body)
}
