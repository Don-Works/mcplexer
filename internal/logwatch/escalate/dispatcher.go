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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

// Throttle defaults — layered storm-proofing on top of mesh-trigger
// throttle_seconds.
const (
	perTemplateCooldown = time.Hour
	maxNotifiesPerHour  = 6
	// maxHumanPushesPerHour bounds durable human (PWA/Web Push) alerts
	// per workspace. The one-shot dedupe below already collapses repeats
	// of the SAME incident; this cap keeps a burst of DISTINCT new-critical
	// templates from becoming one lock-screen buzz each — the same
	// storm-proofing the channel path already gets.
	maxHumanPushesPerHour = 6
	// maxTrackedHumanIncidents caps the one-shot dedupe map so a daemon
	// that runs for years can't leak unboundedly. Critical incidents are
	// rare; on overflow we reset the map (a long-resolved incident
	// re-alerting once is harmless).
	maxTrackedHumanIncidents = 20000
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

// HumanPublisher is the daemon's durable Signal + Web Push bus. Monitoring
// publishes only newly discovered critical incidents here; ordinary updates
// remain in configured channels and never generate another human interruption.
type HumanPublisher interface {
	Publish(evt notify.Event)
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
	human       HumanPublisher
	gatewayHost string
	publicURL   string

	mu             sync.Mutex
	perTemplate    map[string]time.Time // ws+template → last notify
	perHour        map[string][]time.Time
	humanIncidents map[string]string      // ws+template → canonical task id (empty until triage)
	humanPerHour   map[string][]time.Time // ws → recent human-push times (hourly cap)
	now            func() time.Time
}

// RegisterSender attaches (or replaces) the sender for one channel
// kind after construction — used by daemon wiring for senders whose
// dependencies come up later (telegram manager, downstream bridge).
func (d *Dispatcher) RegisterSender(kind string, s Sender) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.senders[kind] = s
}

// RegisterHumanPublisher attaches the daemon's notification bus after boot
// wiring has created it. Re-registering the same live bus is harmless.
func (d *Dispatcher) RegisterHumanPublisher(p HumanPublisher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.human = p
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
		publicURL: normalizePublicURL(firstNonEmpty(
			os.Getenv("MCPLEXER_PUBLIC_URL"), os.Getenv("MCPLEXER_EXTERNAL_URL"))),
		perTemplate: map[string]time.Time{}, perHour: map[string][]time.Time{},
		humanIncidents: map[string]string{},
		humanPerHour:   map[string][]time.Time{},
		now:            time.Now,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func normalizePublicURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return raw
}

func taskURL(publicURL, workspaceID, taskID string) string {
	if publicURL == "" || workspaceID == "" || taskID == "" {
		return ""
	}
	return publicURL + "/tasks/" + url.PathEscape(taskID) +
		"?workspace=" + url.QueryEscape(workspaceID)
}

func humanIncidentLink(n distill.Notification) string {
	if n.TaskID != "" {
		link := "/tasks/" + url.PathEscape(n.TaskID)
		if n.WorkspaceID != "" {
			link += "?workspace=" + url.QueryEscape(n.WorkspaceID)
		}
		return link
	}
	if n.WorkspaceID != "" {
		return "/monitoring?workspace=" + url.QueryEscape(n.WorkspaceID)
	}
	return "/monitoring"
}

func humanIncidentEvent(workspaceName string, n distill.Notification, at time.Time) notify.Event {
	source := firstNonEmpty(n.SourceName, n.RemoteHostName)
	body := "New critical monitoring incident"
	if source != "" {
		body += " from " + source
	}
	if n.TaskID != "" {
		body += ". Tap to open task " + n.TaskID + "."
	} else {
		body += ". Tap to review Monitoring."
	}
	idKind, id := "template", n.TemplateID
	if n.TaskID != "" {
		idKind, id = "task", n.TaskID
	}
	return notify.Event{
		MessageID: "monitoring_critical:" + n.WorkspaceID + ":" + idKind + ":" + id,
		Source:    "monitoring",
		AgentName: "log-watch",
		Role:      "incident",
		Kind:      "monitoring_critical_new",
		Priority:  "critical",
		Title:     "CRITICAL · " + firstNonEmpty(workspaceName, "unknown-system") + " · " + strings.TrimSpace(n.Title),
		Body:      body,
		Tags:      "monitoring,logwatch,critical,new," + n.WorkspaceID,
		Link:      humanIncidentLink(n),
		CreatedAt: at.UTC(),
	}
}

// claimHumanIncident enforces one human interruption for the first critical
// observation of an incident. A deterministic pre-triage alert has no task id;
// when the worker later attaches the canonical task we associate it without a
// second push. A later regression with a different canonical task may alert.
func (d *Dispatcher) claimHumanIncident(n distill.Notification) (HumanPublisher, bool) {
	if !n.NewIncident || n.Severity != store.SeverityCritical ||
		(n.TemplateID == "" && n.TaskID == "") {
		return nil, false
	}
	key := n.WorkspaceID + "/" + n.TemplateID
	if n.TemplateID == "" {
		key = n.WorkspaceID + "/task/" + n.TaskID
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.human == nil {
		return nil, false
	}
	previousTask, seen := d.humanIncidents[key]
	if !seen {
		if len(d.humanIncidents) >= maxTrackedHumanIncidents {
			slog.Warn("escalate: human-incident dedupe map reset at cap",
				"cap", maxTrackedHumanIncidents)
			d.humanIncidents = map[string]string{}
		}
		d.humanIncidents[key] = n.TaskID
		return d.human, true
	}
	if previousTask == "" && n.TaskID != "" {
		d.humanIncidents[key] = n.TaskID
		return d.human, false
	}
	if previousTask != "" && n.TaskID != "" && previousTask != n.TaskID {
		d.humanIncidents[key] = n.TaskID
		return d.human, true
	}
	return d.human, false
}

// RenderMessage keeps chat alerts compact and scannable while preserving
// deterministic context. Google Chat text messages support lightweight
// Markdown and <url|label> links, which gives us a useful task link without
// the visual weight of a card or decorative emoji.
func RenderMessage(workspaceName, gatewayHost, publicURL string, n distill.Notification) string {
	workspaceName = firstNonEmpty(workspaceName, "unknown-system")
	gatewayHost = firstNonEmpty(gatewayHost, "mcplexer")

	var b strings.Builder
	fmt.Fprintf(&b, "*%s · %s*\n%s", upper(n.Severity), workspaceName, strings.TrimSpace(n.Title))

	contextLines := make([]string, 0, 3)
	if n.RemoteHostName != "" || n.RemoteHostAddr != "" {
		host := n.RemoteHostName
		if host == "" {
			host = n.RemoteHostAddr
		} else if n.RemoteHostAddr != "" {
			host += " (" + n.RemoteHostAddr + ")"
		}
		contextLines = append(contextLines, "*Host:* "+host)
	}
	if n.SourceName != "" {
		contextLines = append(contextLines, "*Source:* `"+n.SourceName+"`")
	}
	contextLines = append(contextLines, "*Watcher:* `"+gatewayHost+"`")
	if len(contextLines) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(contextLines, "\n"))
	}

	if body := strings.TrimSpace(n.Body); body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}

	footer := make([]string, 0, 2)
	if n.TaskID != "" {
		if link := taskURL(normalizePublicURL(publicURL), n.WorkspaceID, n.TaskID); link != "" {
			footer = append(footer, "*Task:* <"+link+"|"+n.TaskID+">")
		} else {
			footer = append(footer, "*Task:* `"+n.TaskID+"`")
		}
	}
	if n.TemplateID != "" {
		footer = append(footer, "*Template:* `"+n.TemplateID+"`")
	}
	if len(footer) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(footer, "\n"))
	}
	return b.String()
}

// RenderPlainMessage is the deterministic plaintext render for channels
// that do NOT speak Google Chat markup — telegram, whatsapp, mesh, and
// the dashboards/peers that surface mesh text. It leads with the ratified
// envelope line, then title, source, body, and bare task/template refs,
// so channel-specific markup (`*bold*`, `<url|label>`) never leaks as
// literal noise on a channel that can't render it.
func RenderPlainMessage(workspaceName, gatewayHost, publicURL string, n distill.Notification) string {
	workspaceName = firstNonEmpty(workspaceName, "unknown-system")
	gatewayHost = firstNonEmpty(gatewayHost, "mcplexer")

	var b strings.Builder
	b.WriteString(Envelope(workspaceName, gatewayHost, n.Severity, n.RemoteHostName, n.RemoteHostAddr))
	if title := strings.TrimSpace(n.Title); title != "" {
		b.WriteString("\n")
		b.WriteString(title)
	}
	if n.SourceName != "" {
		b.WriteString("\n\nSource: ")
		b.WriteString(n.SourceName)
	}
	if body := strings.TrimSpace(n.Body); body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	footer := make([]string, 0, 2)
	if n.TaskID != "" {
		if link := taskURL(normalizePublicURL(publicURL), n.WorkspaceID, n.TaskID); link != "" {
			footer = append(footer, "Task: "+link)
		} else {
			footer = append(footer, "Task: "+n.TaskID)
		}
	}
	if n.TemplateID != "" {
		footer = append(footer, "Template: "+n.TemplateID)
	}
	if len(footer) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(footer, "\n"))
	}
	return b.String()
}

// Envelope renders the deterministic first line every plaintext channel
// carries (design §5.6): workspace + gateway host + affected host. Google
// Chat webhooks use the richer RenderMessage instead.
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
	var suppressed string
	if n.Test {
		n.Title = "[test] " + n.Title
	} else {
		suppressed = d.throttled(n.WorkspaceID, n.TemplateID)
	}
	humanCandidate := n.NewIncident && n.Severity == store.SeverityCritical &&
		(n.TemplateID != "" || n.TaskID != "")
	if suppressed != "" && !humanCandidate {
		slog.Info("escalate: suppressed", "workspace", n.WorkspaceID,
			"template", n.TemplateID, "reason", suppressed)
		return nil
	}

	ws, err := d.store.GetWorkspace(ctx, n.WorkspaceID)
	if err != nil {
		return fmt.Errorf("escalate: workspace: %w", err)
	}
	if publisher, claimed := d.claimHumanIncident(n); claimed {
		if d.allowHumanPush(n.WorkspaceID) {
			publisher.Publish(humanIncidentEvent(ws.Name, n, d.now()))
		} else {
			slog.Info("escalate: human push suppressed by hourly cap",
				"workspace", n.WorkspaceID, "template", n.TemplateID)
		}
	}
	if suppressed != "" {
		slog.Info("escalate: channels suppressed after human escalation", "workspace", n.WorkspaceID,
			"template", n.TemplateID, "reason", suppressed)
		return nil
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
		sender, ok := d.sender(ch.Kind)
		if !ok {
			slog.Warn("escalate: no sender wired for channel kind",
				"kind", ch.Kind, "channel", ch.Name)
			continue
		}
		if err := sender.Send(ctx, ch, n.Severity, d.renderForChannel(ch.Kind, ws.Name, n)); err != nil {
			slog.Warn("escalate: send failed", "channel", ch.Name, "kind", ch.Kind, "error", err)
			continue
		}
		sent++
	}
	if sent > 0 && !n.Test {
		d.recordNotify(n.WorkspaceID, n.TemplateID)
	}
	return nil
}

// renderForChannel picks the right formatting per channel kind: Google
// Chat webhooks get lightweight Markdown + <url|label> links; every other
// kind (telegram, whatsapp, mesh) gets deterministic plaintext so
// channel-specific markup never reaches a channel that can't render it.
func (d *Dispatcher) renderForChannel(kind, workspaceName string, n distill.Notification) string {
	if kind == store.ChannelKindGChatWebhook {
		return RenderMessage(workspaceName, d.gatewayHost, d.publicURL, n)
	}
	return RenderPlainMessage(workspaceName, d.gatewayHost, d.publicURL, n)
}

// allowHumanPush enforces the per-workspace hourly ceiling on durable
// human (PWA/Web Push) alerts. It prunes lapsed entries, and records the
// push iff it is admitted — check and record are one locked step so a
// concurrent storm can't overshoot the cap.
func (d *Dispatcher) allowHumanPush(workspaceID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	recent := d.humanPerHour[workspaceID][:0]
	for _, t := range d.humanPerHour[workspaceID] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxHumanPushesPerHour {
		d.humanPerHour[workspaceID] = recent
		return false
	}
	d.humanPerHour[workspaceID] = append(recent, now)
	return true
}

// sender is the lock-guarded senders lookup (RegisterSender may run
// after the dispatcher is live).
func (d *Dispatcher) sender(kind string) (Sender, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.senders[kind]
	return s, ok
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
	// Evict per-template entries whose cooldown has already lapsed so the
	// map can't grow without bound on a long-lived daemon. A lapsed entry
	// gates nothing, so dropping it changes no decision. Notifies are rare
	// (hourly-capped), so the full-map sweep here is cheap.
	for k, t := range d.perTemplate {
		if now.Sub(t) >= perTemplateCooldown {
			delete(d.perTemplate, k)
		}
	}
}
