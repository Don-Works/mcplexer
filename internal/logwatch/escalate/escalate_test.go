package escalate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// TestEnvelope pins the exact deterministic format (design §5.6).
func TestEnvelope(t *testing.T) {
	got := Envelope("example-system", "lxc-mcplexer", "critical", "ip-prod-1", "100.64.0.3")
	want := "[example-system · via lxc-mcplexer] CRITICAL · ip-prod-1 (100.100.0.3)"
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

func testNotification(sev, tpl string) distill.Notification {
	return distill.Notification{
		WorkspaceID: "ws", Severity: sev, Title: "new error-class template",
		Body: "template: pgx refused", RemoteHostName: "ip-prod-1",
		RemoteHostAddr: "100.64.0.3", TemplateID: tpl,
	}
}

func newTestDispatcher(channels []*store.MonitoringChannel, senders map[string]Sender) (*Dispatcher, *time.Time) {
	d := NewDispatcher(&fakeDispatchStore{channels: channels}, senders)
	d.gatewayHost = "lxc-mcplexer"
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
	if !strings.HasPrefix(feed.got[0], "[example-system · via lxc-mcplexer] ERROR · ip-prod-1 (100.100.0.3)") {
		t.Fatalf("payload missing envelope: %q", feed.got[0])
	}

	if err := d.Notify(context.Background(), testNotification(store.SeverityCritical, "t2")); err != nil {
		t.Fatal(err)
	}
	if len(incidents.got) != 1 {
		t.Fatalf("critical must reach incidents: %d", len(incidents.got))
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
	msg := "[example-system · via lxc-mcplexer] CRITICAL · ip-prod-1 (100.100.0.3)\nboom"
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
