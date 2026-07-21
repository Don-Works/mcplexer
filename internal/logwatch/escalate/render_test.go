package escalate

import (
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

func TestEnvelope(t *testing.T) {
	got := Envelope("example-system", "lxc-mcplexer", "critical", "prod-1", "203.0.113.10")
	want := "[example-system · via lxc-mcplexer] CRITICAL · prod-1 (203.0.113.10)"
	if got != want {
		t.Fatalf("envelope:\n got %q\nwant %q", got, want)
	}
}

func TestRenderMessage_ReadableClickableTaskID(t *testing.T) {
	n := distill.Notification{
		WorkspaceID: "acme-ws", Severity: store.SeverityError,
		Title: "Purchase order creation failed", Body: "Three upstream requests failed; successful orders are still flowing.",
		TaskID: "01KXJJD5C0ANQ5GWV8D1NS793P", RemoteHostName: "acme-production",
		RemoteHostAddr: "203.0.113.71", SourceName: "acme-production",
		TemplateID: "f8ece4f0688e6e487943ed079c7f4947",
	}
	got := RenderMessage("acme-prod", "log-watcher", "https://monitor.example/", n)
	want := "*ERROR · acme-prod*\nPurchase order creation failed\n\n" +
		"*Host:* acme-production (203.0.113.71)\n*Source:* `acme-production`\n" +
		"*Watcher:* `log-watcher`\n\nThree upstream requests failed; successful orders are still flowing.\n\n" +
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
	if strings.ContainsAny(got, "*`|") {
		t.Fatalf("plaintext render leaked chat markup: %q", got)
	}
	if !strings.HasPrefix(got, "[acme-prod · via log-watcher] ERROR · acme-production (203.0.113.71)\nPurchase order creation failed") {
		t.Fatalf("plain envelope: %q", got)
	}
	if !strings.Contains(got, "Task: https://monitor.example/tasks/01KXJJD5C0ANQ5GWV8D1NS793P?workspace=acme-ws") || !strings.Contains(got, "Template: abc123") {
		t.Fatalf("plain footer: %q", got)
	}
}

func TestRenderMessage_TaskIDFallbackWithoutPublicURL(t *testing.T) {
	n := distill.Notification{WorkspaceID: "ws", Severity: store.SeverityWarn,
		Title: "Collector window incomplete", TaskID: "task-123"}
	got := RenderMessage("example-system", "log-watcher", "", n)
	if !strings.Contains(got, "*Task:* `task-123`") || strings.Contains(got, "<http") {
		t.Fatalf("task fallback: %q", got)
	}
}
