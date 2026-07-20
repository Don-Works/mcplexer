package escalate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	maxSendAttempts = 3
	retryBaseDelay  = 200 * time.Millisecond
)

// temporarySendError marks a failure that is safe to retry within the same
// bounded notification attempt. Its text must never contain channel secrets.
type temporarySendError struct{ err error }

func (e temporarySendError) Error() string   { return e.err.Error() }
func (e temporarySendError) Unwrap() error   { return e.err }
func (e temporarySendError) Temporary() bool { return true }

func transient(err error) error { return temporarySendError{err: err} }

func (d *Dispatcher) sendWithRetry(
	ctx context.Context,
	sender Sender,
	channel *store.MonitoringChannel,
	severity, message string,
) error {
	var err error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		err = sender.Send(ctx, channel, severity, message)
		if err == nil || !isTemporary(err) || attempt == maxSendAttempts {
			return err
		}
		slog.Warn("escalate: transient send failed; retrying",
			"channel", channel.Name, "kind", channel.Kind, "attempt", attempt)
		if err := d.retryPause(ctx, retryBaseDelay*time.Duration(attempt)); err != nil {
			return err
		}
	}
	return err
}

func isTemporary(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var marked interface{ Temporary() bool }
	if errors.As(err, &marked) && marked.Temporary() {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && network.Timeout()
}

func pauseWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Dispatcher) deliveryResult(n distill.Notification, o Outcome) error {
	if store.SeverityRank(n.Severity) < store.SeverityRank(store.SeverityError) {
		return nil
	}
	if n.NewIncident || n.Test {
		return newIncidentResult(n, o)
	}
	return d.reminderResult(n, o)
}

// newIncidentResult is the pre-existing contract for a first notification,
// unchanged: anything short of one accepted route is an error, so the
// distiller leaves its hysteresis latch unarmed and retries the episode.
func newIncidentResult(n distill.Notification, o Outcome) error {
	if o.Delivered > 0 {
		if len(o.Failures) > 0 {
			slog.Warn("escalate: incident delivered with degraded routes",
				"workspace", n.WorkspaceID, "incident", n.IncidentID,
				"delivered", o.Delivered, "failed_routes", len(o.Failures))
		}
		return nil
	}
	return notAcceptedError(n, o)
}

// reminderResult is the contract for a re-notification from the persistence
// sweep. It used to return nil unconditionally, which meant the sweep marked
// the incident notified — advancing its backoff — whether or not anything was
// actually sent. A silently failing channel was therefore indistinguishable
// from success, which is the exact shape of "nobody told me".
func (d *Dispatcher) reminderResult(n distill.Notification, o Outcome) error {
	key := n.WorkspaceID + "/" + notificationDeliveryKey(n)
	switch o.Status {
	case StatusNotAttempted:
		// No route was eligible. Legitimate: the incident sits below every
		// channel's min_severity floor, or none is configured. Marking it
		// notified is correct — re-firing every 5m would help nobody.
		slog.Info("escalate: reminder had no eligible route",
			"workspace", n.WorkspaceID, "incident", n.IncidentID, "severity", n.Severity)
	case StatusSuppressed:
		// The throttle did its job, and nil is honest here for one specific
		// reason: recordNotify writes a throttle mark ONLY when a channel
		// actually accepted the message (outcome.sent > 0). A reminder can
		// therefore only be suppressed because a real delivery for this
		// incident landed inside the cooldown, or because the workspace has
		// already spent its critical budget on real deliveries this hour.
		// Suppression is thus evidence the operator WAS told — the opposite of
		// a failure — so advancing the backoff is correct. Were that invariant
		// broken (a mark written without a send), this case would silently
		// consume reminders again, which is the original defect. Callers that
		// need to see the suppression can read Outcome.Status.
	case StatusFailed:
		return d.reminderNotDelivered(n, o, key)
	}
	d.clearReminderFailure(key)
	return nil
}

// reminderNotDelivered decides whether a failed or throttled reminder is worth
// retrying. Not marking loses nothing and retries in 5m; marking on failure
// loses the reminder outright. The middle is a bounded retry: fail loudly for
// maxReminderDeliveryRetries ticks, then release so the incident's backoff
// advances and the policy schedules a fresh reminder rather than the sweep
// hammering a dead route every 5 minutes for the rest of the day.
func (d *Dispatcher) reminderNotDelivered(n distill.Notification, o Outcome, key string) error {
	attempt := d.recordReminderFailure(key)
	if attempt > maxReminderDeliveryRetries {
		d.clearReminderFailure(key)
		slog.Error("escalate: reminder undeliverable; releasing to policy backoff",
			"workspace", n.WorkspaceID, "incident", n.IncidentID,
			"severity", n.Severity, "status", string(o.Status),
			"attempts", attempt-1, "routes", strings.Join(o.Failures, "; "))
		return nil
	}
	slog.Error("escalate: operator not notified of unresolved incident",
		"workspace", n.WorkspaceID, "incident", n.IncidentID,
		"severity", n.Severity, "status", string(o.Status),
		"attempt", attempt, "routes", strings.Join(o.Failures, "; "))
	return notAcceptedError(n, o)
}

func notAcceptedError(n distill.Notification, o Outcome) error {
	failures := o.Failures
	if len(failures) == 0 {
		failures = []string{"no eligible delivery route"}
	}
	return fmt.Errorf("escalate: %s delivery not accepted: %s",
		n.Severity, strings.Join(failures, "; "))
}
