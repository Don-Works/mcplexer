package escalate

// notify_logging_test.go covers what the dispatcher says about itself. Before
// these, every slog call in the delivery path was a failure or a suppression:
// "did an alert reach a human?" was unanswerable from the system's own output,
// and absence of failure lines was mistaken for absence of delivery.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// captureLogs redirects slog for one test and restores it afterwards.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

func countLines(logs *bytes.Buffer, needle string) int {
	return strings.Count(logs.String(), needle)
}

const (
	deliveredLine  = `msg="escalate: delivered"`
	brokenLine     = `msg="escalate: channel appears broken`
	recoveredLine  = `msg="escalate: channel recovered"`
	suppressedLine = `msg="escalate: suppressed"`
)

// TestDispatcher_SuccessfulDeliveryIsLogged: one structured line per genuine
// delivery, carrying the fields an operator needs to answer "was I told?".
func TestDispatcher_SuccessfulDeliveryIsLogged(t *testing.T) {
	logs := captureLogs(t)
	sender := &captureSender{}
	d, _ := newTestDispatcher(errorFloorChannel(), map[string]Sender{store.ChannelKindMesh: sender})

	if err := d.Notify(context.Background(), reminder(store.SeverityCritical, "incident-log")); err != nil {
		t.Fatal(err)
	}

	if got := countLines(logs, deliveredLine); got != 1 {
		t.Fatalf("delivered lines=%d want 1\n%s", got, logs.String())
	}
	for _, field := range []string{
		"outcome=delivered", "workspace=ws", "incident=incident-log",
		"severity=critical", "channel=incidents", "kind=" + store.ChannelKindMesh,
	} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("delivery log missing %q\n%s", field, logs.String())
		}
	}
}

// TestDispatcher_DeliveryLoggedPerDeliveryNotPerEvaluation: the success line
// must not turn a fan-out into a chatty hot loop — one per accepted route, and
// nothing at all for a route below its floor or for a suppressed notification.
func TestDispatcher_DeliveryLoggedPerDeliveryNotPerEvaluation(t *testing.T) {
	logs := captureLogs(t)
	mesh, gchat := &captureSender{}, &captureSender{}
	d, _ := newTestDispatcher([]*store.MonitoringChannel{
		{Name: "ops-feed", Kind: store.ChannelKindMesh, MinSeverity: store.SeverityInfo,
			Enabled: true, WorkspaceID: "ws"},
		{Name: "pager", Kind: store.ChannelKindGChatWebhook, MinSeverity: store.SeverityCritical,
			Enabled: true, WorkspaceID: "ws"},
		{Name: "muted", Kind: store.ChannelKindMesh, MinSeverity: store.SeverityInfo,
			Enabled: false, WorkspaceID: "ws"},
	}, map[string]Sender{
		store.ChannelKindMesh: mesh, store.ChannelKindGChatWebhook: gchat,
	})

	// Error clears only the mesh floor: exactly one delivery, one line.
	if err := d.Notify(context.Background(), reminder(store.SeverityError, "incident-a")); err != nil {
		t.Fatal(err)
	}
	if got := countLines(logs, deliveredLine); got != 1 {
		t.Fatalf("one eligible route must log once, got %d\n%s", got, logs.String())
	}
	// The same notification again is suppressed by the cooldown: no route is
	// tried, so nothing may claim a delivery.
	if err := d.Notify(context.Background(), reminder(store.SeverityError, "incident-a")); err != nil {
		t.Fatal(err)
	}
	if got := countLines(logs, deliveredLine); got != 1 {
		t.Fatalf("suppressed notification must not log a delivery, got %d\n%s", got, logs.String())
	}
	// Critical clears both floors: two more deliveries, two more lines.
	if err := d.Notify(context.Background(), reminder(store.SeverityCritical, "incident-b")); err != nil {
		t.Fatal(err)
	}
	if got := countLines(logs, deliveredLine); got != 3 {
		t.Fatalf("two eligible routes must log twice more, got %d\n%s", got, logs.String())
	}
}

// TestDispatcher_PersistentlyBrokenChannelSurfacesThroughSuppression is the
// six-day gchat webhook. A route that rejects every message logged "send
// failed" exactly once and was then masked by the workspace hourly cap, which
// withholds the whole notification before any channel is consulted. A
// suppression mechanism hid a failure; the two are not the same thing.
// pagerAndFeedChannels is the production shape: gchat at the warn floor (the
// route that pages the operator) alongside mesh at info (the route that keeps
// succeeding, so every notification still looks delivered).
func pagerAndFeedChannels() []*store.MonitoringChannel {
	return []*store.MonitoringChannel{
		{Name: "pager", Kind: store.ChannelKindGChatWebhook, MinSeverity: store.SeverityWarn,
			Enabled: true, WorkspaceID: "ws"},
		{Name: "ops-feed", Kind: store.ChannelKindMesh, MinSeverity: store.SeverityInfo,
			Enabled: true, WorkspaceID: "ws"},
	}
}

func warnNotification(templateID string) distill.Notification {
	return distill.Notification{
		WorkspaceID: "ws", Severity: store.SeverityWarn,
		Title: "disk pressure", Body: "still climbing", TemplateID: templateID,
	}
}

func TestDispatcher_PersistentlyBrokenChannelSurfacesThroughSuppression(t *testing.T) {
	logs := captureLogs(t)
	broken, working := &failingSender{}, &captureSender{}
	d, now := newTestDispatcher(pagerAndFeedChannels(), map[string]Sender{
		store.ChannelKindGChatWebhook: broken, store.ChannelKindMesh: working,
	})
	d.retryPause = func(context.Context, time.Duration) error { return nil }

	send := func(template string) {
		t.Helper()
		if err := d.Notify(context.Background(), warnNotification(template)); err != nil {
			t.Fatalf("warn notification %s: %v", template, err)
		}
	}

	// Distinct templates, so the per-template cooldown never bites and the
	// workspace hourly cap is what eventually suppresses.
	for i := range 10 {
		send("tpl-" + string(rune('a'+i)))
	}

	if got := countLines(logs, suppressedLine); got == 0 {
		t.Fatalf("expected the hourly cap to suppress later notifications\n%s", logs.String())
	}
	if got := countLines(logs, brokenLine); got != 1 {
		t.Fatalf("broken channel reports=%d want 1\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "consecutive_failures=3") {
		t.Fatalf("broken-channel report must state the failure run\n%s", logs.String())
	}
	// Every notification still "succeeded" via mesh — which is exactly why the
	// per-notification outcome could never have surfaced this.
	if len(working.got) == 0 {
		t.Fatal("mesh deliveries should have succeeded throughout")
	}

	// An hour on, still broken, still suppressed for most ticks: the route must
	// say so again rather than going quiet for six days.
	*now = now.Add(61 * time.Minute)
	for i := range 3 {
		send("tpl-next-" + string(rune('a'+i)))
	}
	if got := countLines(logs, brokenLine); got != 2 {
		t.Fatalf("broken channel must re-report on the next interval, got %d\n%s",
			got, logs.String())
	}
}

// TestDispatcher_ChannelHealthKeyedByStableID: renaming a channel in the UI
// must not reset its failure run. Health keys on the channel ID because a reset
// run reads as recovery, and a route that is still broken announcing recovery
// is worse than saying nothing.
func TestDispatcher_ChannelHealthKeyedByStableID(t *testing.T) {
	logs := captureLogs(t)
	channels := []*store.MonitoringChannel{{
		ID: "chan-01", Name: "pager", Kind: store.ChannelKindMesh,
		MinSeverity: store.SeverityError, Enabled: true, WorkspaceID: "ws",
	}}
	d, _ := newTestDispatcher(channels, map[string]Sender{store.ChannelKindMesh: &failingSender{}})
	d.retryPause = func(context.Context, time.Duration) error { return nil }

	// Two failures under the original name: one short of the threshold.
	for range channelUnhealthyThreshold - 1 {
		_ = d.Notify(context.Background(), reminder(store.SeverityCritical, "incident-rename"))
	}
	if got := countLines(logs, brokenLine); got != 0 {
		t.Fatalf("below threshold must stay quiet: %d\n%s", got, logs.String())
	}

	// Operator renames the channel. Same row, same ID, same broken endpoint.
	channels[0].Name = "pager-primary"
	_ = d.Notify(context.Background(), reminder(store.SeverityCritical, "incident-rename"))

	if got := countLines(logs, brokenLine); got != 1 {
		t.Fatalf("rename reset the failure run — the route is still broken: %d\n%s",
			got, logs.String())
	}
	if !strings.Contains(logs.String(), "consecutive_failures=3") {
		t.Fatalf("failure run must carry across the rename\n%s", logs.String())
	}
}

// TestDispatcher_ChannelHealthRecovery: a blip must not be reported as broken,
// and a route that comes back must say so.
func TestDispatcher_ChannelHealthRecovery(t *testing.T) {
	logs := captureLogs(t)
	// Fails twice (below the threshold), then recovers: no broken report.
	sender := &scriptedSender{errs: []error{
		errUnavailable(), errUnavailable(), nil,
	}}
	d, now := newTestDispatcher(errorFloorChannel(), map[string]Sender{store.ChannelKindMesh: sender})
	d.retryPause = func(context.Context, time.Duration) error { return nil }

	for range 3 {
		*now = now.Add(20 * time.Minute)
		_ = d.Notify(context.Background(), reminder(store.SeverityCritical, "incident-blip"))
	}
	if got := countLines(logs, brokenLine); got != 0 {
		t.Fatalf("two failures is a blip, not a broken channel: %d\n%s", got, logs.String())
	}
	if got := countLines(logs, recoveredLine); got != 0 {
		t.Fatalf("a route that was never declared broken must not announce recovery\n%s",
			logs.String())
	}

	// Now drive it past the threshold and back again.
	failing := &failingSender{}
	d2, clock := newTestDispatcher(errorFloorChannel(), map[string]Sender{store.ChannelKindMesh: failing})
	d2.retryPause = func(context.Context, time.Duration) error { return nil }
	for range channelUnhealthyThreshold {
		*clock = clock.Add(20 * time.Minute)
		_ = d2.Notify(context.Background(), reminder(store.SeverityCritical, "incident-down"))
	}
	if got := countLines(logs, brokenLine); got != 1 {
		t.Fatalf("threshold crossing must report once: %d\n%s", got, logs.String())
	}
	d2.RegisterSender(store.ChannelKindMesh, &captureSender{})
	*clock = clock.Add(20 * time.Minute)
	if err := d2.Notify(context.Background(), reminder(store.SeverityCritical, "incident-down")); err != nil {
		t.Fatalf("recovered route: %v", err)
	}
	if got := countLines(logs, recoveredLine); got != 1 {
		t.Fatalf("recovery must be stated once: %d\n%s", got, logs.String())
	}
}

func errUnavailable() error { return errors.New("endpoint temporarily unavailable") }
