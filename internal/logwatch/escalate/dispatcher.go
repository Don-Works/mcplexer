// Package escalate is the Monitoring notification dispatcher — the
// ONLY send path (ratified 2026-07-08). It renders the deterministic
// envelope, resolves channel secret refs daemon-side, enforces
// throttles, and fans out to every enabled channel whose min_severity
// admits the incident. The log-watch worker holds no channel tools —
// it can only reach the world through monitoring.notify → here.
package escalate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// Throttle defaults — layered storm-proofing on top of mesh-trigger
// throttle_seconds.
const (
	perTemplateCooldown = time.Hour
	maxNotifiesPerHour  = 6
)

// Store is the dispatcher's slice of store.Store.
type Store interface {
	ListMonitoringChannels(ctx context.Context, workspaceID string) ([]*store.MonitoringChannel, error)
	GetWorkspace(ctx context.Context, id string) (*store.Workspace, error)
}

// Sender delivers one rendered message over one channel kind.
// severity is the incident's level (already reflected in the message
// envelope) for senders that map it onto their own priority scheme.
type Sender interface {
	Send(ctx context.Context, ch *store.MonitoringChannel, severity, message string) error
}

// Result reports where one notification went.
type Result struct {
	Delivered  []string `json:"delivered"`  // channel names
	Failed     []string `json:"failed"`     // channel names with send errors
	Suppressed string   `json:"suppressed"` // non-empty = throttle reason, nothing sent
}

// Dispatcher owns envelope + throttle + fan-out state.
type Dispatcher struct {
	store       Store
	senders     map[string]Sender // by channel kind
	gatewayHost string

	mu          sync.Mutex
	perTemplate map[string]time.Time // ws+template → last notify
	perHour     map[string][]time.Time
	now         func() time.Time
}

// NewDispatcher wires the dispatcher. Kinds with no registered sender
// are skipped with a warning at notify time (e.g. whatsapp before the
// downstream bridge is wired).
func NewDispatcher(st Store, senders map[string]Sender) *Dispatcher {
	host, _ := os.Hostname()
	if host == "" {
		host = "mcplexer"
	}
	return &Dispatcher{
		store: st, senders: senders, gatewayHost: host,
		perTemplate: map[string]time.Time{}, perHour: map[string][]time.Time{},
		now: time.Now,
	}
}

// Envelope renders the deterministic first line every outbound message
// carries (design §5.6): workspace + gateway host + affected host.
func Envelope(workspaceName, gatewayHost, severity, remoteHostName, remoteHostAddr string) string {
	host := remoteHostName
	if remoteHostAddr != "" {
		host += " (" + remoteHostAddr + ")"
	}
	if host == "" {
		host = "-"
	}
	return fmt.Sprintf("[%s · via %s] %s · %s",
		workspaceName, gatewayHost, upper(severity), host)
}

func upper(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		}
	}
	return string(out)
}

// Notify implements distill.Notifier: throttle → envelope → fan-out.
func (d *Dispatcher) Notify(ctx context.Context, n distill.Notification) error {
	if !store.ValidSeverity(n.Severity) {
		return fmt.Errorf("escalate: invalid severity %q", n.Severity)
	}
	if reason := d.throttled(n.WorkspaceID, n.TemplateID); reason != "" {
		slog.Info("escalate: suppressed", "workspace", n.WorkspaceID,
			"template", n.TemplateID, "reason", reason)
		return nil
	}

	ws, err := d.store.GetWorkspace(ctx, n.WorkspaceID)
	if err != nil {
		return fmt.Errorf("escalate: workspace: %w", err)
	}
	message := Envelope(ws.Name, d.gatewayHost, n.Severity, n.RemoteHostName, n.RemoteHostAddr) +
		"\n" + n.Title
	if n.Body != "" {
		message += "\n" + n.Body
	}

	channels, err := d.store.ListMonitoringChannels(ctx, n.WorkspaceID)
	if err != nil {
		return fmt.Errorf("escalate: channels: %w", err)
	}
	rank := store.SeverityRank(n.Severity)
	sent := 0
	for _, ch := range channels {
		if !ch.Enabled || store.SeverityRank(ch.MinSeverity) > rank {
			continue
		}
		sender, ok := d.senders[ch.Kind]
		if !ok {
			slog.Warn("escalate: no sender wired for channel kind",
				"kind", ch.Kind, "channel", ch.Name)
			continue
		}
		if err := sender.Send(ctx, ch, n.Severity, message); err != nil {
			slog.Warn("escalate: send failed", "channel", ch.Name, "kind", ch.Kind, "error", err)
			continue
		}
		sent++
	}
	if sent > 0 {
		d.recordNotify(n.WorkspaceID, n.TemplateID)
	}
	return nil
}

// throttled returns a human-readable suppression reason or "".
func (d *Dispatcher) throttled(workspaceID, templateID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if templateID != "" {
		if last, ok := d.perTemplate[workspaceID+"/"+templateID]; ok &&
			now.Sub(last) < perTemplateCooldown {
			return "per-template cooldown"
		}
	}
	recent := d.perHour[workspaceID][:0]
	for _, t := range d.perHour[workspaceID] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	d.perHour[workspaceID] = recent
	if len(recent) >= maxNotifiesPerHour {
		return "workspace hourly notify cap"
	}
	return ""
}

func (d *Dispatcher) recordNotify(workspaceID, templateID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if templateID != "" {
		d.perTemplate[workspaceID+"/"+templateID] = now
	}
	d.perHour[workspaceID] = append(d.perHour[workspaceID], now)
}
