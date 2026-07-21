// guardrails_test.go — finish-what-you-start guardrails: offer TTL
// sweep, stale-task aging summary, the skipped-review nudge, the
// review-kind lifecycle semantics (not working, not terminal), and the
// degraded-mode schema probe.
package tasks_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

func seedOffer(t *testing.T, db *sqlite.DB, direction, nonce string, age time.Duration, state string) string {
	t.Helper()
	o := &store.TaskOffer{
		RemoteTaskID:      "remote-" + nonce,
		FromPeerID:        "peer-A",
		ToPeerID:          "peer-B",
		RemoteWorkspaceID: "ws-remote",
		Title:             "offer " + nonce,
		EnvelopeNonce:     nonce,
		Direction:         direction,
		State:             state,
		CreatedAt:         time.Now().UTC().Add(-age),
	}
	if err := db.CreateTaskOffer(context.Background(), o); err != nil {
		t.Fatalf("seed offer %s: %v", nonce, err)
	}
	return o.ID
}

func TestSweepExpiredOffers(t *testing.T) {
	ctx := context.Background()
	svc, db, _ := newSvc(t)

	staleOut := seedOffer(t, db, "outgoing", "n1", 8*24*time.Hour, store.TaskOfferPending)
	freshOut := seedOffer(t, db, "outgoing", "n2", 2*24*time.Hour, store.TaskOfferPending)
	staleIn := seedOffer(t, db, "incoming", "n3", 25*time.Hour, store.TaskOfferPending)
	freshIn := seedOffer(t, db, "incoming", "n4", 2*time.Hour, store.TaskOfferPending)
	oldAccepted := seedOffer(t, db, "incoming", "n5", 30*24*time.Hour, store.TaskOfferAccepted)

	n, err := svc.SweepExpiredOffers(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredOffers: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 expired, got %d", n)
	}
	wantStates := map[string]string{
		staleOut:    store.TaskOfferExpired,
		freshOut:    store.TaskOfferPending,
		staleIn:     store.TaskOfferExpired,
		freshIn:     store.TaskOfferPending,
		oldAccepted: store.TaskOfferAccepted,
	}
	for id, want := range wantStates {
		o, gerr := db.GetTaskOffer(ctx, id)
		if gerr != nil {
			t.Fatalf("GetTaskOffer %s: %v", id, gerr)
		}
		if o.State != want {
			t.Errorf("offer %s state = %q, want %q", id, o.State, want)
		}
		if want == store.TaskOfferExpired && !strings.Contains(o.DeclinedReason, "expired by TTL sweep") {
			t.Errorf("offer %s missing audit stamp, declined_reason = %q", id, o.DeclinedReason)
		}
	}
	// Idempotent: a second sweep finds nothing pending-and-stale.
	n2, err := svc.SweepExpiredOffers(ctx)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second sweep expired %d, want 0", n2)
	}
}

func seedAgedTask(t *testing.T, db *sqlite.DB, wsID, title, status, assignee string, age time.Duration) *store.Task {
	t.Helper()
	then := time.Now().UTC().Add(-age)
	task := &store.Task{
		WorkspaceID:       wsID,
		Title:             title,
		Status:            status,
		AssigneeSessionID: assignee,
		CreatedAt:         then,
		UpdatedAt:         then,
	}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task %q: %v", title, err)
	}
	return task
}

func TestStaleTasksSummary(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	seedAgedTask(t, db, wsID, "review stale", "review", "", 30*time.Hour)
	seedAgedTask(t, db, wsID, "review fresh", "review", "", 2*time.Hour)
	seedAgedTask(t, db, wsID, "blocked stale", "blocked", "", 100*time.Hour)
	seedAgedTask(t, db, wsID, "blocked fresh", "blocked", "", 10*time.Hour)
	oldestSeed := seedAgedTask(t, db, wsID, "claimed and abandoned", "open", "sess-x", 8*24*time.Hour)
	seedAgedTask(t, db, wsID, "open unassigned old", "open", "", 9*24*time.Hour)
	seedAgedTask(t, db, wsID, "working old (lease machinery owns it)", "doing", "sess-y", 9*24*time.Hour)

	sum, err := svc.StaleTasks(ctx, wsID)
	if err != nil {
		t.Fatalf("StaleTasks: %v", err)
	}
	if sum == nil {
		t.Fatal("expected non-nil summary")
	}
	if sum.Count != 3 {
		t.Fatalf("count = %d, want 3 (review>24h, blocked>72h, assigned-open>7d)", sum.Count)
	}
	if sum.Oldest.ID != oldestSeed.ID {
		t.Errorf("oldest.id = %s, want %s (the 8d assigned-open row)", sum.Oldest.ID, oldestSeed.ID)
	}
	if sum.Oldest.Status != "open" {
		t.Errorf("oldest.status = %q, want open", sum.Oldest.Status)
	}
	if sum.Oldest.AgeHours < 8*24-1 || sum.Oldest.AgeHours > 8*24+1 {
		t.Errorf("oldest.age_hours = %d, want ~%d", sum.Oldest.AgeHours, 8*24)
	}
	if sum.Hint == "" {
		t.Error("expected non-empty hint")
	}
}

func TestStaleTasksNilWhenNothingStale(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	seedAgedTask(t, db, wsID, "fresh review", "review", "", time.Hour)
	sum, err := svc.StaleTasks(ctx, wsID)
	if err != nil {
		t.Fatalf("StaleTasks: %v", err)
	}
	if sum != nil {
		t.Fatalf("expected nil summary, got %+v", sum)
	}
}

func TestStaleTasksRespectsDeclaredVocabKinds(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	if err := db.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "awaiting_review", Kind: "review",
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab: %v", err)
	}
	seedAgedTask(t, db, wsID, "custom review word", "awaiting_review", "", 30*time.Hour)
	sum, err := svc.StaleTasks(ctx, wsID)
	if err != nil {
		t.Fatalf("StaleTasks: %v", err)
	}
	if sum == nil || sum.Count != 1 {
		t.Fatalf("expected 1 stale via declared review-kind vocab, got %+v", sum)
	}
}

func TestUpdateWithSignalsReviewSkippedNudge(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		start      string
		viaReview  bool
		next       string
		wantNudge  bool
		wantSignal bool
	}{
		{name: "working to terminal without review nudges", start: "doing", next: "done", wantNudge: true, wantSignal: true},
		{name: "working to terminal after review does not nudge", start: "doing", viaReview: true, next: "done"},
		{name: "non-working to terminal does not nudge", start: "blocked", next: "done"},
		{name: "working to working does not nudge", start: "doing", next: "triaging"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, wsID := newSvc(t)
			row, err := svc.Create(ctx, tasks.CreateOptions{
				WorkspaceID:        wsID,
				Title:              tt.name,
				Status:             tt.start,
				CreatedBySessionID: "test-session",
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if tt.viaReview {
				review := "review"
				row, _, err = svc.UpdateWithSignals(ctx, wsID, row.ID, tasks.UpdatePatch{
					Status:             &review,
					UpdatedBySessionID: "test-session",
				})
				if err != nil {
					t.Fatalf("UpdateWithSignals review: %v", err)
				}
			}
			next := tt.next
			_, signals, err := svc.UpdateWithSignals(ctx, wsID, row.ID, tasks.UpdatePatch{
				Status:             &next,
				UpdatedBySessionID: "test-session",
			})
			if err != nil {
				t.Fatalf("UpdateWithSignals final: %v", err)
			}
			if gotSignal := signals != nil; gotSignal != tt.wantSignal {
				t.Fatalf("signals present = %v, want %v (%+v)", gotSignal, tt.wantSignal, signals)
			}
			if signals != nil {
				if signals.ReviewSkipped != tt.wantNudge {
					t.Fatalf("ReviewSkipped = %v, want %v", signals.ReviewSkipped, tt.wantNudge)
				}
				if tt.wantNudge && signals.ReviewSkippedHint == "" {
					t.Fatal("expected review skipped hint")
				}
			}
		})
	}
}

// failingProbeStore overrides the schema probe with a hard failure so
// the degraded-mode flag can be pinned without corrupting a real DB.
type failingProbeStore struct{ *sqlite.DB }

func (failingProbeStore) ProbeTaskSchema(context.Context) error {
	return errors.New("SQL logic error: no such column: hlc_at")
}

func TestSchemaProbeDegradedFlag(t *testing.T) {
	_, db, _ := newSvc(t)
	if err := tasks.New(db).SchemaErr(); err != nil {
		t.Fatalf("healthy schema should probe clean, got: %v", err)
	}
	err := tasks.New(failingProbeStore{db}).SchemaErr()
	if err == nil {
		t.Fatal("expected SchemaErr on failing probe")
	}
	if !strings.Contains(err.Error(), "tasks schema probe failed") {
		t.Fatalf("expected wrapped probe error, got: %v", err)
	}
}
