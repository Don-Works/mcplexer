package escalate

// outcome.go — the honest answer to "was the operator actually told?".
//
// Notify's error return alone could not answer that. It returned nil for every
// non-new incident, so a re-notification that reached no route at all was
// indistinguishable from one that reached every route. The renotify sweep marks
// an incident notified on nil and advances its backoff, so a silently failing
// channel looked exactly like success and the reminder was consumed. Given the
// operator's complaint about the 2026-07-20 incident was "nobody told me", that
// is the failure mode this file exists to make impossible.

import "log/slog"

// DeliveryStatus is the four-state verdict on one notification.
type DeliveryStatus string

const (
	// StatusDelivered: at least one route accepted the message.
	StatusDelivered DeliveryStatus = "delivered"
	// StatusNotAttempted: no route was eligible — every configured channel sits
	// above this severity, or none is configured. A legitimate no-op rather than
	// a failure: nothing was tried, so nothing was lost, and marking the
	// incident notified is correct.
	StatusNotAttempted DeliveryStatus = "not_attempted"
	// StatusFailed: a route was eligible and tried, and every attempt failed.
	// The operator was NOT told.
	StatusFailed DeliveryStatus = "failed"
	// StatusSuppressed: the dispatcher's own throttle withheld the message
	// before any route was tried. Also not-told, but costing no I/O to retry.
	StatusSuppressed DeliveryStatus = "suppressed"
)

// Outcome reports where one notification actually went. Callers that need more
// than "error or not" (the renotify sweep especially, which decides whether to
// advance an incident's backoff) should read this rather than infer from nil.
type Outcome struct {
	Status DeliveryStatus
	// Delivered counts routes that accepted the message.
	Delivered int
	// Attempted counts routes that were eligible and tried, accepted or not.
	Attempted int
	// Failures describes each route-level problem. Never carries channel
	// secrets — safe to log and to surface in the UI.
	Failures []string
	// Suppressed carries the throttle reason when Status is StatusSuppressed.
	Suppressed string
}

// Told reports whether this notification reached a human-visible route. The
// question the operator actually asks, in one call.
func (o Outcome) Told() bool { return o.Status == StatusDelivered }

// deliveryOutcome is the mutable accumulator the fan-out writes into.
type deliveryOutcome struct {
	delivered int
	attempted int
	sent      int
	failures  []string
}

func (o *deliveryOutcome) status() DeliveryStatus {
	switch {
	case o.delivered > 0:
		return StatusDelivered
	case o.attempted > 0:
		return StatusFailed
	default:
		return StatusNotAttempted
	}
}

func (o *deliveryOutcome) result() Outcome {
	return Outcome{
		Status: o.status(), Delivered: o.delivered,
		Attempted: o.attempted, Failures: o.failures,
	}
}

// recordReminderFailure counts consecutive failed reminder deliveries for one
// incident and reports the attempt number. The count exists to bound retries: a
// route that has been dead for half an hour will not come back because we asked
// it 288 more times today.
func (d *Dispatcher) recordReminderFailure(key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reminderFailures) >= maxTrackedReminderFailures {
		slog.Warn("escalate: reminder-failure map reset at cap", "cap", maxTrackedReminderFailures)
		d.reminderFailures = map[string]int{}
	}
	d.reminderFailures[key]++
	return d.reminderFailures[key]
}

// clearReminderFailure re-arms the retry budget. Called on delivery, on a
// legitimate no-op, and when the budget is released to the policy — so the next
// reminder the policy schedules gets a full set of attempts of its own.
func (d *Dispatcher) clearReminderFailure(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.reminderFailures, key)
}
