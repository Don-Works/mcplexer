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
		"*Watcher:* `log-watcher`\n\n*Evidence:* Three upstream requests failed; successful orders are still flowing.\n\n" +
		"*Task:* <https://monitor.example/tasks/01KXJJD5C0ANQ5GWV8D1NS793P?workspace=acme-ws|01KXJJD5C0ANQ5GWV8D1NS793P>"
	if got != want {
		t.Fatalf("rendered message:\n got %q\nwant %q", got, want)
	}
}

func TestRenderMessage_CompactsStructuredEvidenceForOperator(t *testing.T) {
	n := distill.Notification{
		WorkspaceID: "ws", Severity: store.SeverityCritical,
		Title: "Order sync has stopped", TaskID: "task-123",
		Body: "Observed evidence\n- No successful sync has completed for 43 minutes.\n" +
			"- The process is still running.\n\nVerified facts\n- Queue depth is 842.\n\n" +
			"Hypotheses / unknowns\n- The worker may be wedged. " +
			strings.Repeat("additional diagnostic detail ", 30) + "SECRET-TAIL",
	}
	got := RenderMessage("orders", "log-watcher", "https://monitor.example", n)
	if !strings.Contains(got, "*Evidence:* No successful sync has completed for 43 minutes. · The process is still running. · Queue depth is 842.") {
		t.Fatalf("evidence was not flattened into an operator summary: %q", got)
	}
	if strings.Contains(got, "Observed evidence") || strings.Contains(got, "Hypotheses / unknowns") ||
		strings.Contains(got, "worker may be wedged") || strings.Contains(got, "SECRET-TAIL") {
		t.Fatalf("structured report leaked into compact Chat alert: %q", got)
	}
	if !strings.Contains(got, "*Task:* <https://monitor.example/tasks/task-123?workspace=ws|task-123>") {
		t.Fatalf("compact alert lost the task link: %q", got)
	}
	if len([]rune(got)) > 650 {
		t.Fatalf("Chat alert is not compact: %d runes", len([]rune(got)))
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
