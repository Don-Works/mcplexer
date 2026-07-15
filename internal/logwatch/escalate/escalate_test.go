package escalate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

// TestEnvelope pins the exact deterministic format (design §5.6).
func TestEnvelope(t *testing.T) {
	got := Envelope("example-system", "lxc-mcplexer", "critical", "prod-1", "203.0.113.10")
	want := "[example-system · via lxc-mcplexer] CRITICAL · prod-1 (203.0.113.10)"
	if got != want {
		t.Fatalf("envelope:\n got %q\nwant %q", got, want)
	}
}

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

type captureHumanPublisher struct{ got []notify.Event }

func (c *captureHumanPublisher) Publish(evt notify.Event) {
	c.got = append(c.got, evt)
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

func TestRenderMessage_ReadableClickableTaskID(t *testing.T) {
	n := distill.Notification{
		WorkspaceID: "acme-ws", Severity: store.SeverityError,
		Title:          "Purchase order creation failed",
		Body:           "Three upstream requests failed; successful orders are still flowing.",
		TaskID:         "01KXJJD5C0ANQ5GWV8D1NS793P",
		RemoteHostName: "acme-production", RemoteHostAddr: "203.0.113.71",
		SourceName: "acme-production", TemplateID: "f8ece4f0688e6e487943ed079c7f4947",
	}
	got := RenderMessage("acme-prod", "log-watcher",
		"https://monitor.example/", n)
	want := "*ERROR · acme-prod*\n" +
		"Purchase order creation failed\n\n" +
		"*Host:* acme-production (203.0.113.71)\n" +
		"*Source:* `acme-production`\n" +
		"*Watcher:* `log-watcher`\n\n" +
		"Three upstream requests failed; successful orders are still flowing.\n\n" +
		"*Task:* <https://monitor.example/tasks/01KXJJD5C0ANQ5GWV8D1NS793P?workspace=acme-ws|01KXJJD5C0ANQ5GWV8D1NS793P>\n" +
		"*Template:* `f8ece4f0688e6e487943ed079c7f4947`"
	if got != want {
		t.Fatalf("rendered message:\n got %q\nwant %q", got, want)
	}
}

func TestRenderPlainMessage_NoChannelSpecificMarkup(t *testing.T) {
	n := distill.Notification{
		WorkspaceID: "acme-ws", Severity: store.SeverityError,
		Title: "Purchase order creation failed", Body: "Three upstream requests failed.",
		TaskID: "01KXJJD5C0ANQ5GWV8D1NS793P", RemoteHostName: "acme-production",
		RemoteHostAddr: "203.0.113.71", SourceName: "acme-production", TemplateID: "abc123",
	}
	got := RenderPlainMessage("acme-prod", "log-watcher", "https://monitor.example/", n)
	// Plaintext channels must never receive Google Chat markup as literal noise.
	if strings.ContainsAny(got, "*`|") {
		t.Fatalf("plaintext render leaked chat markup: %q", got)
	}
	if !strings.HasPrefix(got, "[acme-prod · via log-watcher] ERROR · acme-production (203.0.113.71)\nPurchase order creation failed") {
		t.Fatalf("plain envelope: %q", got)
	}
	if !strings.Contains(got, "Task: https://monitor.example/tasks/01KXJJD5C0ANQ5GWV8D1NS793P?workspace=acme-ws") {
		t.Fatalf("plain task link: %q", got)
	}
	if !strings.Contains(got, "Template: abc123") {
		t.Fatalf("plain template ref: %q", got)
	}
}

func TestRenderMessage_TaskIDFallbackWithoutPublicURL(t *testing.T) {
	n := distill.Notification{
		WorkspaceID: "ws", Severity: store.SeverityWarn,
		Title: "Collector window incomplete", TaskID: "task-123",
	}
	got := RenderMessage("example-system", "log-watcher", "", n)
	if !strings.Contains(got, "*Task:* `task-123`") || strings.Contains(got, "<http") {
		t.Fatalf("task fallback: %q", got)
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
		if err := d.Notify(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	if len(human.got) != maxHumanPushesPerHour {
		t.Fatalf("distinct-critical storm: want %d human pushes, got %d",
			maxHumanPushesPerHour, len(human.got))
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

type fakeSecrets struct{ url string }

func (f fakeSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte(f.url), nil
}

// TestGChatWebhookSender posts {"text": message} to the resolved
// webhook — the URL never appears in the channel row.
func TestGChatWebhookSender(t *testing.T) {
	var received map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &GChatWebhookSender{Secrets: fakeSecrets{url: srv.URL}}
	ch := &store.MonitoringChannel{
		Name: "incidents", Kind: store.ChannelKindGChatWebhook, WorkspaceID: "ws",
		ConfigJSON: `{"auth_scope_id":"scope1","webhook_ref":"secret://GCHAT_WEBHOOK_INCIDENTS"}`,
	}
	msg := "[example-system · via lxc-mcplexer] CRITICAL · prod-1 (203.0.113.10)\nboom"
	if err := s.Send(context.Background(), ch, store.SeverityCritical, msg); err != nil {
		t.Fatal(err)
	}
	if received["text"] != msg {
		t.Fatalf("webhook payload: %+v", received)
	}

	// Missing scope/ref must fail loudly, not fall back to plaintext.
	bad := &store.MonitoringChannel{Name: "b", ConfigJSON: `{"webhook_ref":"https://plain.example"}`}
	if err := s.Send(context.Background(), bad, store.SeverityError, "x"); err == nil {
		t.Fatal("plaintext/missing-scope config must be rejected")
	}
}

type fakeTGBridge struct{ chatID, text, priority string }

func (f *fakeTGBridge) SendByChatID(_ context.Context, chatID, text, priority string) error {
	f.chatID, f.text, f.priority = chatID, text, priority
	return nil
}

// TestTelegramSender maps severity onto mesh priority vocabulary and
// targets the configured chat.
func TestTelegramSender(t *testing.T) {
	bridge := &fakeTGBridge{}
	s := &TelegramSender{Bridge: bridge}
	ch := &store.MonitoringChannel{Name: "tg", ConfigJSON: `{"chat_id":"chat-42"}`}
	if err := s.Send(context.Background(), ch, store.SeverityCritical, "msg"); err != nil {
		t.Fatal(err)
	}
	if bridge.chatID != "chat-42" || bridge.priority != "critical" || bridge.text != "msg" {
		t.Fatalf("bridge got %+v", bridge)
	}
}

type fakeCaller struct {
	name string
	args map[string]string
}

func (f *fakeCaller) CallTool(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	f.name = name
	_ = json.Unmarshal(args, &f.args)
	return json.RawMessage(`{}`), nil
}

// TestWhatsAppSender passes the secret:// ref VERBATIM so the gateway
// substitutes at dispatch — the number never exists here.
func TestWhatsAppSender(t *testing.T) {
	caller := &fakeCaller{}
	s := &WhatsAppSender{Caller: caller}
	ch := &store.MonitoringChannel{Name: "wa",
		ConfigJSON: `{"chat_id_ref":"secret://WHATSAPP_PERSONAL_CHAT_ID","session_id":"main"}`}
	if err := s.Send(context.Background(), ch, store.SeverityCritical, "boom"); err != nil {
		t.Fatal(err)
	}
	if caller.name != "openwa__send_text" ||
		caller.args["chat_id"] != "secret://WHATSAPP_PERSONAL_CHAT_ID" ||
		caller.args["session_id"] != "main" || caller.args["text"] != "boom" {
		t.Fatalf("caller got %s %+v", caller.name, caller.args)
	}

	bad := &store.MonitoringChannel{Name: "bad", ConfigJSON: `{"chat_id_ref":"447700900000@c.us"}`}
	if err := s.Send(context.Background(), bad, store.SeverityCritical, "x"); err == nil {
		t.Fatal("plaintext chat id must be rejected")
	}
}
