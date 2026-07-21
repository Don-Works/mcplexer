// concierge_test.go — unit coverage for the classifier + the Record /
// MarkPromoted round-trip against a real sqlite store.
package concierge_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestService(t *testing.T) (*concierge.Service, *sqlite.DB) {
	t.Helper()
	d, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return concierge.NewService(d, nil), d
}

func TestRuleClassifier(t *testing.T) {
	c := concierge.NewRuleClassifier()
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"explicit-wrong", "no, that's wrong", store.ChatTurnLabelCorrection},
		{"actually-prefix", "actually, I meant the other one", store.ChatTurnLabelCorrection},
		{"nope-prefix", "nope. try again", store.ChatTurnLabelCorrection},
		{"thanks", "thanks! perfect.", store.ChatTurnLabelConfirmation},
		{"yes-prefix", "yes, that's exactly right", store.ChatTurnLabelConfirmation},
		{"frustration-fuck", "fuck this", store.ChatTurnLabelFrustration},
		{"frustration-bangs", "what??!! that's insane!!", store.ChatTurnLabelFrustration},
		{"escalation", "can I talk to a human please", store.ChatTurnLabelEscalation},
		{"redirect", "ok, now let's switch to the deploy question", store.ChatTurnLabelRedirect},
		{"neutral", "the deploy timestamp is 2026-05-26", store.ChatTurnLabelNeutral},
		{"empty", "", store.ChatTurnLabelNeutral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Classify(tt.msg, "")
			if got.Label != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.msg, got.Label, tt.want)
			}
			if got.Confidence <= 0 || got.Confidence > 1 {
				t.Errorf("Classify confidence out of range: %v", got.Confidence)
			}
			if got.Kind != store.ChatTurnClassifierRule {
				t.Errorf("Classify kind = %q, want rule", got.Kind)
			}
		})
	}
}

func TestRecordRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, db := newTestService(t)

	row, err := svc.Record(ctx, concierge.RecordOptions{
		WorkerID:         "wkr-test-123",
		WorkspaceID:      "ws-test",
		UserIDExternal:   "telegram:42",
		Channel:          "telegram",
		PromptVersion:    1,
		TurnID:           "run-abc",
		UserMessage:      "no, that's wrong",
		AssistantMessage: "the deploy ran yesterday at 9am UTC",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if row.Label != store.ChatTurnLabelCorrection {
		t.Errorf("auto-classified label = %q, want correction", row.Label)
	}
	if row.ID == "" {
		t.Error("row.ID was not populated")
	}

	// List filtered by NotPromoted + correction should surface this row.
	rows, err := db.ListChatTurnSignals(ctx, store.ChatTurnSignalFilter{
		WorkerID:    "wkr-test-123",
		Labels:      []string{store.ChatTurnLabelCorrection},
		NotPromoted: true,
	})
	if err != nil {
		t.Fatalf("ListChatTurnSignals: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != row.ID {
		t.Fatalf("expected 1 row matching the inserted id, got %d rows", len(rows))
	}

	// MarkPromoted then re-list: should disappear from the not-promoted view.
	if err := svc.MarkPromoted(ctx, row.ID, "refinement-xyz"); err != nil {
		t.Fatalf("MarkPromoted: %v", err)
	}
	rows, err = db.ListChatTurnSignals(ctx, store.ChatTurnSignalFilter{
		WorkerID:    "wkr-test-123",
		Labels:      []string{store.ChatTurnLabelCorrection},
		NotPromoted: true,
	})
	if err != nil {
		t.Fatalf("ListChatTurnSignals (after promote): %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows after promote, got %d", len(rows))
	}
}

func TestRecordExplicitLabelOverride(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	row, err := svc.Record(ctx, concierge.RecordOptions{
		WorkerID:    "wkr-test",
		Channel:     "telegram",
		UserMessage: "this would normally classify as correction (no)",
		Label:       store.ChatTurnLabelNeutral,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if row.Label != store.ChatTurnLabelNeutral {
		t.Errorf("explicit label = %q, want neutral", row.Label)
	}
}

func TestRecordTruncatesLongMessages(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	huge := strings.Repeat("a", 10_000)
	row, err := svc.Record(ctx, concierge.RecordOptions{
		WorkerID:    "wkr-test",
		Channel:     "telegram",
		UserMessage: huge,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(row.UserMessage) > 2*1024+10 {
		t.Errorf("UserMessage not truncated: len=%d", len(row.UserMessage))
	}
}

func TestRecordRejectsMissingFields(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	cases := []concierge.RecordOptions{
		{Channel: "telegram", UserMessage: "x"}, // no worker_id
		{WorkerID: "w", UserMessage: "x"},       // no channel
		{WorkerID: "w", Channel: "telegram"},    // no user_message
	}
	for i, opts := range cases {
		if _, err := svc.Record(ctx, opts); err == nil {
			t.Errorf("case %d should have failed but didn't", i)
		}
	}
}
