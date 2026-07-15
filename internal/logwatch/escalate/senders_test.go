package escalate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

type fakeSecrets struct{ url string }

func (f fakeSecrets) Get(context.Context, string, string) ([]byte, error) {
	return []byte(f.url), nil
}

func TestGChatWebhookSender(t *testing.T) {
	var received map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sender := &GChatWebhookSender{Secrets: fakeSecrets{url: srv.URL}}
	channel := &store.MonitoringChannel{
		Name: "incidents", Kind: store.ChannelKindGChatWebhook, WorkspaceID: "ws",
		ConfigJSON: `{"auth_scope_id":"scope1","webhook_ref":"secret://GCHAT_WEBHOOK_INCIDENTS"}`,
	}
	message := "[example-system · via lxc-mcplexer] CRITICAL · prod-1 (203.0.113.10)\nboom"
	if err := sender.Send(context.Background(), channel, store.SeverityCritical, message); err != nil {
		t.Fatal(err)
	}
	if received["text"] != message {
		t.Fatalf("webhook payload: %+v", received)
	}
	bad := &store.MonitoringChannel{Name: "bad", ConfigJSON: `{"webhook_ref":"https://plain.example"}`}
	if err := sender.Send(context.Background(), bad, store.SeverityError, "x"); err == nil {
		t.Fatal("plaintext/missing-scope config must be rejected")
	}
}

type fakeTGBridge struct{ chatID, text, priority string }

func (f *fakeTGBridge) SendByChatID(_ context.Context, chatID, text, priority string) error {
	f.chatID, f.text, f.priority = chatID, text, priority
	return nil
}

func TestTelegramSender(t *testing.T) {
	bridge := &fakeTGBridge{}
	sender := &TelegramSender{Bridge: bridge}
	channel := &store.MonitoringChannel{Name: "tg", ConfigJSON: `{"chat_id":"chat-42"}`}
	if err := sender.Send(context.Background(), channel, store.SeverityCritical, "msg"); err != nil {
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

func TestWhatsAppSender(t *testing.T) {
	caller := &fakeCaller{}
	sender := &WhatsAppSender{Caller: caller}
	channel := &store.MonitoringChannel{Name: "wa",
		ConfigJSON: `{"chat_id_ref":"secret://WHATSAPP_PERSONAL_CHAT_ID","session_id":"main"}`}
	if err := sender.Send(context.Background(), channel, store.SeverityCritical, "boom"); err != nil {
		t.Fatal(err)
	}
	if caller.name != "openwa__send_text" || caller.args["chat_id"] != "secret://WHATSAPP_PERSONAL_CHAT_ID" ||
		caller.args["session_id"] != "main" || caller.args["text"] != "boom" {
		t.Fatalf("caller got %s %+v", caller.name, caller.args)
	}
	bad := &store.MonitoringChannel{Name: "bad", ConfigJSON: `{"chat_id_ref":"447700900000@c.us"}`}
	if err := sender.Send(context.Background(), bad, store.SeverityCritical, "x"); err == nil {
		t.Fatal("plaintext chat id must be rejected")
	}
	customTool := &store.MonitoringChannel{Name: "custom", ConfigJSON: `{
		"chat_id_ref":"secret://WHATSAPP_PERSONAL_CHAT_ID","tool":"other__send"
	}`}
	if err := sender.Send(context.Background(), customTool, store.SeverityCritical, "x"); err == nil {
		t.Fatal("arbitrary downstream tool must be rejected")
	}
}
