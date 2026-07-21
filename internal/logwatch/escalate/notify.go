package escalate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

type deliveryOutcome struct {
	delivered int
	sent      int
	failures  []string
}

// Notify implements distill.Notifier: throttle → durable human signal →
// severity-filtered channel fan-out.
func (d *Dispatcher) Notify(ctx context.Context, n distill.Notification) error {
	if !store.ValidSeverity(n.Severity) {
		return fmt.Errorf("escalate: invalid severity %q", n.Severity)
	}
	suppressed := d.prepareNotification(&n)
	if suppressed != "" && !isHumanCandidate(n) {
		slog.Info("escalate: suppressed", "workspace", n.WorkspaceID,
			"template", n.TemplateID, "reason", suppressed)
		// Route through deliveryResult rather than returning nil: a suppressed
		// NEW error incident (delivered=0) must propagate an error so the
		// distiller leaves the hysteresis latch UNARMED and retries once the
		// throttle budget frees — returning nil here silently armed the latch
		// and lost the episode. Info / non-new traffic still returns nil via
		// deliveryResult's severity/NewIncident gate.
		return d.deliveryResult(n, 0, []string{"channels: " + suppressed})
	}
	workspace, err := d.store.GetWorkspace(ctx, n.WorkspaceID)
	if err != nil {
		return fmt.Errorf("escalate: workspace: %w", err)
	}
	outcome := &deliveryOutcome{}
	d.deliverHuman(ctx, workspace.Name, n, outcome)
	if suppressed != "" {
		outcome.failures = append(outcome.failures, "channels: "+suppressed)
		return d.deliveryResult(n, outcome.delivered, outcome.failures)
	}
	channels, err := d.store.ListMonitoringChannels(ctx, n.WorkspaceID)
	if err != nil {
		return fmt.Errorf("escalate: channels: %w", err)
	}
	d.deliverChannels(ctx, workspace.Name, n, channels, outcome)
	if outcome.sent > 0 && !n.Test {
		d.recordNotify(n.WorkspaceID, notificationDeliveryKey(n), n.Severity)
	}
	return d.deliveryResult(n, outcome.delivered, outcome.failures)
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
			outcome.delivered++
		}
		return
	}
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
		if !channel.Enabled || store.SeverityRank(channel.MinSeverity) > rank {
			continue
		}
		sender, ok := d.sender(channel.Kind)
		if !ok {
			outcome.failures = append(outcome.failures, channel.Name+": sender unavailable")
			slog.Warn("escalate: no sender wired", "kind", channel.Kind, "channel", channel.Name)
			continue
		}
		message := d.renderForChannel(channel.Kind, workspaceName, n)
		if err := d.sendWithRetry(ctx, sender, channel, n.Severity, message); err != nil {
			outcome.failures = append(outcome.failures, channel.Name+": delivery failed")
			slog.Warn("escalate: send failed", "channel", channel.Name, "kind", channel.Kind, "error", err)
			continue
		}
		outcome.sent++
		outcome.delivered++
	}
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
