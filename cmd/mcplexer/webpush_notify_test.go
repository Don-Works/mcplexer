package main

import (
	"testing"

	"github.com/don-works/mcplexer/internal/notify"
)

func TestWebPushSubscriberSelection(t *testing.T) {
	tests := []struct {
		name      string
		explicit  string
		publicURL string
		want      string
	}{
		{
			name:      "explicit mailto wins",
			explicit:  "mailto:ops@example.com",
			publicURL: "https://app.example.com",
			want:      "mailto:ops@example.com",
		},
		{
			name:      "explicit https wins",
			explicit:  "https://notify.example.com",
			publicURL: "https://app.example.com",
			want:      "https://notify.example.com",
		},
		{
			name:      "public url fallback",
			publicURL: "https://app.example.com",
			want:      "https://app.example.com",
		},
		{
			name:      "bare public host becomes https",
			publicURL: "app.example.com",
			want:      "https://app.example.com",
		},
		{
			name:      "invalid explicit falls back to public url",
			explicit:  "http://insecure.example.com",
			publicURL: "https://app.example.com",
			want:      "https://app.example.com",
		},
		{
			name: "default is valid https uri",
			want: defaultWebPushSubscriber,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webPushSubscriber(tt.explicit, tt.publicURL); got != tt.want {
				t.Fatalf("webPushSubscriber(%q, %q) = %q, want %q", tt.explicit, tt.publicURL, got, tt.want)
			}
		})
	}
}

func TestLoadConfigReadsWebPushSubject(t *testing.T) {
	t.Setenv("MCPLEXER_WEB_PUSH_SUBJECT", "mailto:ops@example.com")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.WebPushSubject != "mailto:ops@example.com" {
		t.Fatalf("WebPushSubject = %q, want %q", cfg.WebPushSubject, "mailto:ops@example.com")
	}
}

func TestWebPushEligibleOnlyForHighSignalEvents(t *testing.T) {
	tests := []struct {
		name string
		evt  notify.Event
		want bool
	}{
		{name: "test push", evt: notify.Event{Kind: "push_test"}, want: true},
		{name: "pending approval", evt: notify.Event{Source: "approval", Kind: "approval_pending"}, want: true},
		{name: "resolved approval", evt: notify.Event{Source: "approval", Kind: "approval_approved"}, want: false},
		{name: "secret prompt", evt: notify.Event{Source: "secret", Kind: "secret_prompt"}, want: true},
		{name: "task assignment", evt: notify.Event{Source: "task", Kind: "task_assigned"}, want: true},
		{name: "task due", evt: notify.Event{Source: "task", Kind: "task_due"}, want: true},
		{name: "generic task update", evt: notify.Event{Source: "task", Kind: "task_updated"}, want: false},
		{name: "legacy task create", evt: notify.Event{Source: "task", Kind: "task_created"}, want: false},
		{name: "memory offer", evt: notify.Event{Source: "memory", Kind: "memory_offer_received"}, want: true},
		{name: "memory write", evt: notify.Event{Source: "memory", Kind: "memory_write"}, want: false},
		{name: "generic delegation completion", evt: notify.Event{Kind: "delegation_completed"}, want: false},
		{name: "normal mesh", evt: notify.Event{Source: "mesh", Kind: "finding", Priority: "normal"}, want: false},
		{name: "high alert", evt: notify.Event{Source: "system", Kind: "alert", Priority: "high"}, want: true},
		{name: "critical mesh", evt: notify.Event{Source: "mesh", Kind: "finding", Priority: "critical"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webPushEligible(tt.evt); got != tt.want {
				t.Fatalf("webPushEligible(%+v) = %v, want %v", tt.evt, got, tt.want)
			}
		})
	}
}

func TestWebPushTaskTopicCollapsesAssignmentIntoDue(t *testing.T) {
	assigned := webPushTopic(notify.Event{
		Source: "task", MessageID: "task_assigned:01TASKABC:100",
	})
	due := webPushTopic(notify.Event{
		Source: "task", MessageID: "task_due:01TASKABC:200",
	})
	if assigned != due {
		t.Fatalf("assignment topic %q != due topic %q", assigned, due)
	}
}

func TestWebPushUrgentUsesHighDeliverySemantics(t *testing.T) {
	evt := notify.Event{Priority: "urgent"}
	if got := webPushTTL(evt); got != 30*60 {
		t.Fatalf("urgent TTL = %d", got)
	}
	if got := webPushUrgency(evt); got != "high" {
		t.Fatalf("urgent urgency = %q", got)
	}
}
