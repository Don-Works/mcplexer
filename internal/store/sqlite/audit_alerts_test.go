package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// insertAuditAt is a tiny helper for the alert tests — one row at a
// timestamp with a given tool/status/latency.
func insertAuditAt(t *testing.T, db *sqlite.DB, ts time.Time, tool, status string, latency int) {
	t.Helper()
	r := &store.AuditRecord{
		Timestamp: ts, CreatedAt: ts,
		ToolName: tool, Status: status, LatencyMs: latency,
		WorkspaceID: "ws1",
	}
	if err := db.InsertAuditRecord(context.Background(), r); err != nil {
		t.Fatalf("insert audit: %v", err)
	}
}

// TestAuditAnomaliesErrorRate seeds a clean baseline window then an
// error-heavy current window and asserts the error-rate anomaly fires.
func TestAuditAnomaliesErrorRate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	window := time.Hour

	// Baseline window [now-2h, now-1h]: 20 calls, all success → 0% errors.
	for i := 0; i < 20; i++ {
		insertAuditAt(t, db, now.Add(-90*time.Minute), "flaky__tool", "success", 10)
	}
	// Current window [now-1h, now]: 10 calls, 8 errors → 80% errors.
	for i := 0; i < 2; i++ {
		insertAuditAt(t, db, now.Add(-30*time.Minute), "flaky__tool", "success", 10)
	}
	for i := 0; i < 8; i++ {
		insertAuditAt(t, db, now.Add(-30*time.Minute), "flaky__tool", "error", 10)
	}

	alerts, err := db.AuditAnomalies(ctx, "", window)
	if err != nil {
		t.Fatalf("anomalies: %v", err)
	}
	found := false
	for _, a := range alerts {
		if a.Kind == "anomaly" && a.ToolName == "flaky__tool" && a.Count == 8 &&
			a.Filter["status"] == "error" {
			found = true
			if a.Severity != "critical" {
				t.Fatalf("80%% error rate should be critical, got %q", a.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected error-rate anomaly for flaky__tool, got %+v", alerts)
	}
}

// TestAuditAnomaliesQuietGatewayNoAlerts confirms a low-traffic, low-error
// gateway produces no anomaly noise (thresholds gate it out).
func TestAuditAnomaliesQuietGatewayNoAlerts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 3 calls, 1 error in the current window — below anomalyMinErrors (5).
	insertAuditAt(t, db, now.Add(-10*time.Minute), "calm__tool", "error", 5)
	insertAuditAt(t, db, now.Add(-10*time.Minute), "calm__tool", "success", 5)
	insertAuditAt(t, db, now.Add(-10*time.Minute), "calm__tool", "success", 5)

	alerts, err := db.AuditAnomalies(ctx, "", time.Hour)
	if err != nil {
		t.Fatalf("anomalies: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("quiet gateway should produce no anomalies, got %+v", alerts)
	}
}

// TestAuditSecurityEvents seeds blocked + denial_reason + cross_org rows
// and asserts the matching security alerts surface.
func TestAuditSecurityEvents(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		r := &store.AuditRecord{
			Timestamp: now.Add(-5 * time.Minute), CreatedAt: now.Add(-5 * time.Minute),
			ToolName: "github__delete_repo", Status: "blocked", WorkspaceID: "ws1",
			DenialReason: "policy_block",
		}
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert blocked: %v", err)
		}
	}
	r := &store.AuditRecord{
		Timestamp: now.Add(-5 * time.Minute), CreatedAt: now.Add(-5 * time.Minute),
		ToolName: "skill__share", Status: "success", WorkspaceID: "ws1",
		Tier: "cross_org",
	}
	if err := db.InsertAuditRecord(ctx, r); err != nil {
		t.Fatalf("insert cross_org: %v", err)
	}

	alerts, err := db.AuditSecurityEvents(ctx, "", time.Hour)
	if err != nil {
		t.Fatalf("security events: %v", err)
	}
	kinds := map[string]int{}
	for _, a := range alerts {
		if a.Kind != "security" {
			t.Fatalf("non-security alert in security events: %+v", a)
		}
		kinds[a.ID] = a.Count
	}
	if kinds["security:blocked:"] != 3 {
		t.Fatalf("blocked count = %d, want 3 (alerts=%+v)", kinds["security:blocked:"], alerts)
	}
	if kinds["security:denial_reason:"] != 3 {
		t.Fatalf("denial_reason count = %d, want 3", kinds["security:denial_reason:"])
	}
	if kinds["security:cross_org:"] != 1 {
		t.Fatalf("cross_org count = %d, want 1", kinds["security:cross_org:"])
	}
}

// TestSavedSearchCRUD exercises create/get/list/update/delete.
func TestSavedSearchCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	s := &store.SavedSearch{
		Name:           "errors on github",
		Q:              "github",
		Filter:         map[string]any{"status": "error"},
		ThresholdCount: 5,
		WindowSec:      600,
		WorkspaceID:    "ws1",
		Enabled:        true,
	}
	if err := db.CreateSavedSearch(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.ID == "" {
		t.Fatal("expected ID assigned")
	}

	got, err := db.GetSavedSearch(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "errors on github" || got.Filter["status"] != "error" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.ThresholdCount != 5 || got.WindowSec != 600 || !got.Enabled {
		t.Fatalf("scalar roundtrip mismatch: %+v", got)
	}

	list, err := db.ListSavedSearches(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	got.Enabled = false
	got.ThresholdCount = 10
	if err := db.UpdateSavedSearch(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, _ := db.GetSavedSearch(ctx, s.ID)
	if reread.Enabled || reread.ThresholdCount != 10 {
		t.Fatalf("post-update mismatch: %+v", reread)
	}

	if err := db.DeleteSavedSearch(ctx, s.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetSavedSearch(ctx, s.ID); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestCountAuditMatching verifies the count honours exact-match + q.
func TestCountAuditMatching(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedAuditSearchRows(t, db)

	errStatus := "error"
	n, err := db.CountAuditMatching(ctx, store.AuditFilter{Status: &errStatus})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("error count = %d, want 2", n)
	}

	// q narrows further.
	n, err = db.CountAuditMatching(ctx, store.AuditFilter{Status: &errStatus, Q: "refused"})
	if err != nil {
		t.Fatalf("count q: %v", err)
	}
	if n != 1 {
		t.Fatalf("error+refused count = %d, want 1", n)
	}
}

// TestEvaluateSavedSearches confirms the evaluator fires when the windowed
// count crosses threshold, debounces afterward, and stamps last_fired_at.
func TestEvaluateSavedSearches(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 6 error rows in the last 10 minutes.
	for i := 0; i < 6; i++ {
		insertAuditAt(t, db, now.Add(-2*time.Minute), "github__x", "error", 10)
	}

	s := &store.SavedSearch{
		Name: "many errors", Q: "", Filter: map[string]any{"status": "error"},
		ThresholdCount: 5, WindowSec: 600, Enabled: true,
	}
	if err := db.CreateSavedSearch(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	fired, err := db.EvaluateSavedSearches(ctx, now)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(fired) != 1 || fired[0].Count != 6 {
		t.Fatalf("expected 1 fired with count 6, got %+v", fired)
	}

	// Debounce: re-evaluating within the window must not re-fire.
	again, err := db.EvaluateSavedSearches(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("evaluate again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("debounce failed — re-fired within window: %+v", again)
	}

	// last_fired_at must be stamped.
	got, _ := db.GetSavedSearch(ctx, s.ID)
	if got.LastFiredAt == nil {
		t.Fatal("last_fired_at not stamped after fire")
	}
}
