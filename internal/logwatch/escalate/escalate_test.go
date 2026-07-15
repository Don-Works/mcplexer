package escalate

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

type fakeDispatchStore struct {
	channels []*store.MonitoringChannel
}

func (f *fakeDispatchStore) ListMonitoringChannels(context.Context, string) ([]*store.MonitoringChannel, error) {
	return f.channels, nil
}
func (f *fakeDispatchStore) GetWorkspace(context.Context, string) (*store.Workspace, error) {
	return &store.Workspace{ID: "ws", Name: "example-system"}, nil
}

type captureSender struct{ got []string }

func (c *captureSender) Send(_ context.Context, _ *store.MonitoringChannel, _, message string) error {
	c.got = append(c.got, message)
	return nil
}

type captureHumanPublisher struct {
	got     []notify.Event
	durable []notify.Event
	err     error
}

func (c *captureHumanPublisher) PublishDurable(_ context.Context, evt notify.Event, interrupt bool) error {
	c.durable = append(c.durable, evt)
	if interrupt {
		c.got = append(c.got, evt)
	}
	return c.err
}

func testNotification(sev, tpl string) distill.Notification {
	return distill.Notification{
		WorkspaceID: "ws", Severity: sev, Title: "new error-class template",
		Body: "template: pgx refused", RemoteHostName: "prod-1",
		RemoteHostAddr: "203.0.113.10", SourceName: "api", TemplateID: tpl,
	}
}

func newTestDispatcher(channels []*store.MonitoringChannel, senders map[string]Sender) (*Dispatcher, *time.Time) {
	d := NewDispatcher(&fakeDispatchStore{channels: channels}, senders)
	d.gatewayHost = "lxc-mcplexer"
	d.publicURL = ""
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	return d, &now
}

// TestDispatcher_SeverityFloorFanout: only channels whose floor admits
// the severity receive, and every payload carries the envelope.
func TestDispatcher_SeverityFloorFanout(t *testing.T) {
	feed := &captureSender{}
	incidents := &captureSender{}
	d, _ := newTestDispatcher([]*store.MonitoringChannel{
		{Name: "ops-feed", Kind: "mesh", MinSeverity: store.SeverityInfo, Enabled: true, WorkspaceID: "ws"},
		{Name: "incidents", Kind: "gchat_webhook", MinSeverity: store.SeverityCritical, Enabled: true, WorkspaceID: "ws"},
		{Name: "muted", Kind: "mesh", MinSeverity: store.SeverityInfo, Enabled: false, WorkspaceID: "ws"},
	}, map[string]Sender{"mesh": feed, "gchat_webhook": incidents})

	if err := d.Notify(context.Background(), testNotification(store.SeverityError, "t1")); err != nil {
		t.Fatal(err)
	}
	if len(feed.got) != 1 || len(incidents.got) != 0 {
		t.Fatalf("error must reach ops-feed only: feed=%d incidents=%d", len(feed.got), len(incidents.got))
	}
	// mesh is a plaintext channel: deterministic envelope, no chat markup.
	if !strings.HasPrefix(feed.got[0], "[example-system · via lxc-mcplexer] ERROR · prod-1 (203.0.113.10)\nnew error-class template") {
		t.Fatalf("mesh payload must be plaintext-enveloped: %q", feed.got[0])
	}
	if strings.ContainsAny(feed.got[0], "*`") {
		t.Fatalf("mesh payload leaked Google Chat markup: %q", feed.got[0])
	}

	if err := d.Notify(context.Background(), testNotification(store.SeverityCritical, "t2")); err != nil {
		t.Fatal(err)
	}
	if len(incidents.got) != 1 {
		t.Fatalf("critical must reach incidents: %d", len(incidents.got))
	}
	// gchat_webhook is the rich channel: Markdown emphasis is expected.
	if !strings.HasPrefix(incidents.got[0], "*CRITICAL · example-system*\nnew error-class template") {
		t.Fatalf("gchat payload must be rich-rendered: %q", incidents.got[0])
	}
}

func TestDispatcher_NewCriticalHumanAlertIsOneShotAndSafe(t *testing.T) {
	d, _ := newTestDispatcher(nil, nil)
	human := &captureHumanPublisher{}
	d.RegisterHumanPublisher(human)
	n := distill.Notification{
		WorkspaceID: "ws", Severity: store.SeverityCritical,
		Title: "Writes are failing", Body: "raw sample must not reach a lock screen",
		SourceName: "acme-production", TemplateID: "tpl-critical",
		NewIncident: true,
	}

	// The deterministic first observation alerts immediately even before
	// triage creates a task, but exposes no raw log body on the lock screen.
	if err := d.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(human.got) != 1 {
		t.Fatalf("new critical alerts = %d, want 1", len(human.got))
	}
	first := human.got[0]
	if first.Priority != "critical" || first.Kind != "monitoring_critical_new" ||
		first.Link != "/monitoring?workspace=ws" {
		t.Fatalf("critical event: %+v", first)
	}
	if strings.Contains(first.Body, "raw sample") {
		t.Fatalf("push body leaked incident evidence: %q", first.Body)
	}

	// Triage associates the canonical task with the already-alerted template
	// without buzzing again. The same task cannot re-alert on evidence updates.
	n.TaskID = "01TASKA"
	if err := d.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	n.NewIncident = false
	if err := d.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(human.got) != 1 {
		t.Fatalf("same incident alerts = %d, want 1", len(human.got))
	}

	// A genuine post-remediation regression gets a different canonical task
	// and is therefore a new human incident even though its template is stable.
	n.NewIncident = true
	n.TaskID = "01TASKB"
	if err := d.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(human.got) != 2 || human.got[1].Link != "/tasks/01TASKB?workspace=ws" {
		t.Fatalf("regression events: %+v", human.got)
	}
}

func TestDispatcher_OngoingOrNonCriticalIncidentDoesNotHumanAlert(t *testing.T) {
	d, _ := newTestDispatcher(nil, nil)
	human := &captureHumanPublisher{}
	d.RegisterHumanPublisher(human)
	for _, n := range []distill.Notification{
		{WorkspaceID: "ws", Severity: store.SeverityCritical, Title: "ongoing", TaskID: "t1"},
		{WorkspaceID: "ws", Severity: store.SeverityError, Title: "new error", TaskID: "t2", NewIncident: true},
	} {
		if err := d.Notify(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	if len(human.got) != 0 {
		t.Fatalf("unexpected human alerts: %+v", human.got)
	}
}

// TestDispatcher_HumanPushHourlyCap: a burst of DISTINCT new-critical
// templates is bounded by the per-workspace hourly ceiling (storm-proofing
// the human path the way the channel path is already bounded), and the
// budget refills on the next hour.
func TestDispatcher_HumanPushHourlyCap(t *testing.T) {
	d, now := newTestDispatcher(nil, nil)
	human := &captureHumanPublisher{}
	d.RegisterHumanPublisher(human)
	for i := range 20 {
		n := distill.Notification{
			WorkspaceID: "ws", Severity: store.SeverityCritical,
			Title: "new critical shape", NewIncident: true,
			TemplateID: "tpl-" + strconv.Itoa(i),
		}
		err := d.Notify(context.Background(), n)
		if i < maxHumanPushesPerHour && err != nil {
			t.Fatal(err)
		}
		if i >= maxHumanPushesPerHour && err == nil {
			t.Fatal("critical without an interrupting route must report degraded delivery")
		}
	}
	if len(human.got) != maxHumanPushesPerHour {
		t.Fatalf("distinct-critical storm: want %d human pushes, got %d",
			maxHumanPushesPerHour, len(human.got))
	}
	if len(human.durable) != 20 {
		t.Fatalf("all critical incidents must remain in durable Signal history: %d", len(human.durable))
	}

	*now = now.Add(61 * time.Minute)
	if err := d.Notify(context.Background(), distill.Notification{
		WorkspaceID: "ws", Severity: store.SeverityCritical, Title: "fresh",
		NewIncident: true, TemplateID: "tpl-fresh",
	}); err != nil {
		t.Fatal(err)
	}
	if len(human.got) != maxHumanPushesPerHour+1 {
		t.Fatalf("human-push cap must reset next hour: got %d", len(human.got))
	}
}

// TestDispatcher_StormThrottled is the M3 storm gate: the same
// template re-firing lands exactly one notification per cooldown, and
// the hourly workspace cap bounds distinct-template storms.
func TestDispatcher_StormThrottled(t *testing.T) {
	sender := &captureSender{}
	d, now := newTestDispatcher([]*store.MonitoringChannel{
		{Name: "feed", Kind: "mesh", MinSeverity: store.SeverityInfo, Enabled: true, WorkspaceID: "ws"},
	}, map[string]Sender{"mesh": sender})

	for range 500 {
		if err := d.Notify(context.Background(), testNotification(store.SeverityError, "same-tpl")); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.got) != 1 {
		t.Fatalf("same-template storm: want exactly 1 send, got %d", len(sender.got))
	}

	// Distinct templates: hourly cap (6) bounds it.
	sender.got = nil
	for i := range 100 {
		n := testNotification(store.SeverityError, "tpl-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := d.Notify(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.got) != maxNotifiesPerHour-1 { // 1 already spent above in the same hour
		t.Fatalf("hourly cap: want %d, got %d", maxNotifiesPerHour-1, len(sender.got))
	}

	// Next hour: capacity returns.
	*now = now.Add(61 * time.Minute)
	if err := d.Notify(context.Background(), testNotification(store.SeverityError, "fresh")); err != nil {
		t.Fatal(err)
	}
	if len(sender.got) != maxNotifiesPerHour {
		t.Fatalf("cap must reset next hour: got %d", len(sender.got))
	}
}
