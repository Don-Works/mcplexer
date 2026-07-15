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

func (d *Dispatcher) criticalDeliveryResult(
	n distill.Notification,
	delivered int,
	failures []string,
) error {
	if n.Severity != store.SeverityCritical || (!n.NewIncident && !n.Test) {
		return nil
	}
	if delivered > 0 {
		if len(failures) > 0 {
			slog.Warn("escalate: critical delivered with degraded routes",
				"workspace", n.WorkspaceID, "delivered", delivered,
				"failed_routes", len(failures))
		}
		return nil
	}
	if len(failures) == 0 {
		failures = []string{"no eligible delivery route"}
	}
	return fmt.Errorf("escalate: critical delivery not accepted: %s", strings.Join(failures, "; "))
}
