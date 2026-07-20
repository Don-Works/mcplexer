package escalate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// Notify implements distill.Notifier. Its error return is deliberately coarse;
// callers that must know whether the operator was actually told should use
// NotifyWithOutcome, which reports delivered / not-attempted / failed /
// suppressed rather than leaving them to be inferred from a nil.
func (d *Dispatcher) Notify(ctx context.Context, n distill.Notification) error {
	_, err := d.NotifyWithOutcome(ctx, n)
	return err
}

// NotifyWithOutcome runs the full path — throttle → durable human signal →
// severity-filtered channel fan-out — and reports where the message went.
func (d *Dispatcher) NotifyWithOutcome(
	ctx context.Context, n distill.Notification,
) (Outcome, error) {
	if !store.ValidSeverity(n.Severity) {
		return Outcome{Status: StatusFailed}, fmt.Errorf("escalate: invalid severity %q", n.Severity)
	}
	suppressed := d.prepareNotification(&n)
	if suppressed != "" && !isHumanCandidate(n) {
		slog.Info("escalate: suppressed", "workspace", n.WorkspaceID,
			"template", n.TemplateID, "incident", n.IncidentID,
			"severity", n.Severity, "reason", suppressed)
		// Route through deliveryResult rather than returning nil: a suppressed
		// NEW error incident (delivered=0) must propagate an error so the
		// distiller leaves the hysteresis latch UNARMED and retries once the
		// throttle budget frees — returning nil here silently armed the latch
		// and lost the episode. A suppressed REMINDER deliberately does NOT
		// propagate (see reminderResult): recordNotify only ever writes a
		// throttle mark after a real send, so suppression of a reminder proves
		// the operator was told recently, and advancing the backoff is right.
		return d.finish(n, Outcome{
			Status: StatusSuppressed, Suppressed: suppressed,
			Failures: []string{"channels: " + suppressed},
		})
	}
	workspace, err := d.store.GetWorkspace(ctx, n.WorkspaceID)
	if err != nil {
		return Outcome{Status: StatusFailed}, fmt.Errorf("escalate: workspace: %w", err)
	}
	outcome := &deliveryOutcome{}
	d.deliverHuman(ctx, workspace.Name, n, outcome)
	if suppressed != "" {
		// The human route ran but the channels were withheld. Keep the reason on
		// the Outcome, not only in Failures, so a caller can tell a throttled
		// fan-out from a route that was tried and refused.
		outcome.failures = append(outcome.failures, "channels: "+suppressed)
		result := outcome.result()
		result.Suppressed = suppressed
		return d.finish(n, result)
	}
	channels, err := d.store.ListMonitoringChannels(ctx, n.WorkspaceID)
	if err != nil {
		return Outcome{Status: StatusFailed}, fmt.Errorf("escalate: channels: %w", err)
	}
	d.deliverChannels(ctx, workspace.Name, n, channels, outcome)
	if outcome.sent > 0 && !n.Test {
		d.recordNotify(n.WorkspaceID, notificationDeliveryKey(n), n.Severity)
	}
	return d.finish(n, outcome.result())
}

// finish pairs the outcome with the error contract the caller expects.
func (d *Dispatcher) finish(n distill.Notification, outcome Outcome) (Outcome, error) {
	return outcome, d.deliveryResult(n, outcome)
}

func (d *Dispatcher) prepareNotification(n *distill.Notification) string {
	if n.Test {
		n.Title = "[test] " + n.Title
		return ""
	}
	return d.throttled(n.WorkspaceID, notificationDeliveryKey(*n), n.Severity)
}

func notificationDeliveryKey(n distill.Notification) string {
	if n.IncidentID != "" {
		return n.IncidentID
	}
	return n.TemplateID
}

func (d *Dispatcher) deliverHuman(
	ctx context.Context, workspaceName string, n distill.Notification, outcome *deliveryOutcome,
) {
	publisher, claim, claimed := d.claimHumanIncident(n)
	if !claimed {
		if isHumanCandidate(n) && d.humanIncidentAccepted(n) {
			outcome.attempted++
			outcome.delivered++
		}
		return
	}
	outcome.attempted++
	reservation, interrupt := d.reserveHumanPush(n.WorkspaceID)
	err := publisher.PublishDurable(ctx, humanIncidentEvent(workspaceName, n, d.now()), interrupt)
	if err != nil {
		d.releaseHumanIncident(claim)
		if interrupt {
			d.releaseHumanPush(n.WorkspaceID, reservation)
		}
		outcome.failures = append(outcome.failures, "human push: delivery failed")
		slog.Warn("escalate: durable human alert failed", "workspace", n.WorkspaceID,
			"template", n.TemplateID, "error", err)
		return
	}
	d.completeHumanIncident(claim, interrupt)
	if interrupt {
		outcome.delivered++
		slog.Info("escalate: delivered", "outcome", "delivered",
			"workspace", n.WorkspaceID, "incident", n.IncidentID,
			"template", n.TemplateID, "severity", n.Severity,
			"channel", "human push", "kind", "human_push")
		return
	}
	outcome.failures = append(outcome.failures, "human push: hourly cap")
	slog.Info("escalate: human push suppressed by hourly cap",
		"workspace", n.WorkspaceID, "template", n.TemplateID)
}

func (d *Dispatcher) deliverChannels(
	ctx context.Context,
	workspaceName string,
	n distill.Notification,
	channels []*store.MonitoringChannel,
	outcome *deliveryOutcome,
) {
	rank := store.SeverityRank(n.Severity)
	for _, channel := range channels {
		// Below the channel's floor is a no-op, not an attempt: nothing was
		// tried, so nothing was lost. Only routes past this gate count as
		// attempted, which is what separates "failed" from "not attempted".
		if !channel.Enabled || store.SeverityRank(channel.MinSeverity) > rank {
			continue
		}
		outcome.attempted++
		reason := d.deliverOne(ctx, workspaceName, n, channel)
		if reason != "" {
			outcome.failures = append(outcome.failures, channel.Name+": "+reason)
			continue
		}
		outcome.sent++
		outcome.delivered++
	}
}

// deliverOne sends to one eligible channel and records that route's health.
// It returns "" on success, or a short reason that never carries a secret.
func (d *Dispatcher) deliverOne(
	ctx context.Context,
	workspaceName string,
	n distill.Notification,
	channel *store.MonitoringChannel,
) string {
	key := channelHealthKey(n.WorkspaceID, channel)
	sender, ok := d.sender(channel.Kind)
	if !ok {
		d.reportChannelFailure(ctx, key, n, channel, "sender unavailable", nil)
		return "sender unavailable"
	}
	message := d.renderForChannel(channel.Kind, workspaceName, n)
	if err := d.sendWithRetry(ctx, sender, channel, n.Severity, message); err != nil {
		d.reportChannelFailure(ctx, key, n, channel, "delivery failed", err)
		return "delivery failed"
	}
	// The success line. Without it the log records only failures and
	// suppressions, and "was anyone actually told?" cannot be answered from the
	// system's own output — absence of a failure is not evidence of delivery.
	// One line per genuine delivery, never per evaluation.
	slog.Info("escalate: delivered", "outcome", "delivered",
		"workspace", n.WorkspaceID, "incident", n.IncidentID,
		"template", n.TemplateID, "severity", n.Severity,
		"channel", channel.Name, "kind", channel.Kind)
	// Every success, not only a recovery: last_success_at is the field an
	// operator reads first, and it is only truthful if each delivery stamps it.
	d.persistChannelSuccess(ctx, channel)
	if health, recovered := d.recordChannelSuccess(key); recovered {
		slog.Info("escalate: channel recovered", "workspace", n.WorkspaceID,
			"channel", channel.Name, "kind", channel.Kind,
			"consecutive_failures", health.consecutiveFailures,
			"was_failing_for", health.failingFor(d.now()).Round(time.Second).String())
	}
	return ""
}

// reportChannelFailure logs the attempt and, once the route's failure run says
// it is broken rather than blipping, escalates to ERROR on a cadence. This is
// what a suppressed notification cannot hide: the run survives across
// notifications even when the throttle stops the route being tried at all.
func (d *Dispatcher) reportChannelFailure(
	ctx context.Context,
	key string, n distill.Notification, channel *store.MonitoringChannel, reason string, err error,
) {
	slog.Warn("escalate: send failed", "outcome", "failed", "reason", reason,
		"workspace", n.WorkspaceID, "incident", n.IncidentID,
		"template", n.TemplateID, "severity", n.Severity,
		"channel", channel.Name, "kind", channel.Kind, "error", err)
	// Persist before the cadence gate, not after it. The ERROR line below is
	// rate-limited to once an hour so it cannot flood the log; the stored
	// counter must see EVERY failure or the run is undercounted and the route
	// takes hours to reach broken instead of three attempts.
	d.persistChannelFailure(ctx, channel, reason, err)
	health, report := d.recordChannelFailure(key)
	if !report {
		return
	}
	slog.Error("escalate: channel appears broken; alerts are not reaching it",
		"workspace", n.WorkspaceID, "channel", channel.Name, "kind", channel.Kind,
		"min_severity", channel.MinSeverity, "reason", reason,
		"consecutive_failures", health.consecutiveFailures,
		"failing_for", health.failingFor(d.now()).Round(time.Second).String(),
		"error", err)
}

func (d *Dispatcher) renderForChannel(kind, workspaceName string, n distill.Notification) string {
	if kind == store.ChannelKindGChatWebhook {
		return RenderMessage(workspaceName, d.gatewayHost, d.publicURL, n)
	}
	return RenderPlainMessage(workspaceName, d.gatewayHost, d.publicURL, n)
}

func (d *Dispatcher) sender(kind string) (Sender, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sender, ok := d.senders[kind]
	return sender, ok
}
