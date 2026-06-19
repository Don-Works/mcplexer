package telegram

import (
	"testing"

	"github.com/don-works/mcplexer/internal/notify"
)

func TestShouldForwardNotifyToTelegram_DropsWorkerLifecycle(t *testing.T) {
	cases := []string{
		"worker,started,worker_started",
		"worker,finished,worker_finished,failure",
		"worker,tool_call,worker_tool_call,task__list",
	}
	for _, tags := range cases {
		if shouldForwardNotifyToTelegram(notify.Event{Tags: tags}) {
			t.Fatalf("worker lifecycle tags should not forward to Telegram: %q", tags)
		}
	}
}

func TestShouldForwardNotifyToTelegram_AllowsActionableWorkerAlerts(t *testing.T) {
	cases := []string{
		"worker,auto_paused",
		"worker,awaiting_approval,worker_awaiting_approval",
		"worker,output,telegram",
	}
	for _, tags := range cases {
		if !shouldForwardNotifyToTelegram(notify.Event{Tags: tags}) {
			t.Fatalf("actionable worker tags should still forward to Telegram: %q", tags)
		}
	}
}

func TestAllowCrossWorkspaceTelegramFallback_CriticalOnly(t *testing.T) {
	if allowCrossWorkspaceTelegramFallback(notify.Event{Priority: "high"}) {
		t.Fatal("high priority should not fan out cross-workspace")
	}
	if !allowCrossWorkspaceTelegramFallback(notify.Event{Priority: "critical"}) {
		t.Fatal("critical priority should still fan out cross-workspace")
	}
}
