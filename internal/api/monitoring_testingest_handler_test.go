// monitoring_testingest_handler_test.go — proof that the gated seeding path
// does what the integration rig needs it to do.
//
// The load-bearing test is TestTestIngestHonoursBackdatedTimestamps. If
// backdated lines did not traverse the real template machinery — if they landed
// as raw rows without templates, or collapsed onto today — then every scenario
// built on seeded history would be asserting on a fiction. So it checks the
// timestamps verbatim AND checks that log_template_days recorded one observed
// day per seeded day, which is the fact absence and baseline detection actually
// read.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

const testIngestMessage = "orders sync complete"

// newTestIngestFixture builds two workspaces so the ownership check has a real
// foreign source to reject, and returns ws-A's source id.
func newTestIngestFixture(t *testing.T, retentionDays int) (*monitoringTestIngestHandler, *sqlite.DB, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test_ingest.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srcA := seedIngestSource(t, db, "ws-A", "orders", retentionDays)
	srcB := seedIngestSource(t, db, "ws-B", "billing", retentionDays)
	return &monitoringTestIngestHandler{store: db}, db, srcA, srcB
}

func seedIngestSource(t *testing.T, db *sqlite.DB, wsID, name string, retentionDays int) string {
	t.Helper()
	ctx := context.Background()
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: wsID, Name: wsID, DefaultPolicy: "allow",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "scope-" + wsID, Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	host := &store.RemoteHost{
		WorkspaceID: wsID, Name: "host-" + wsID, SSHUser: "logwatch",
		SSHHost: "203.0.113.11", SSHPort: 22, AuthScopeID: scope.ID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, host); err != nil {
		t.Fatalf("create host: %v", err)
	}
	src := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: name,
		Kind: store.LogSourceKindDocker, Selector: name, Enabled: true,
		RetentionDays: retentionDays, RetentionMB: 50,
	}
	if err := db.CreateLogSource(ctx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	return src.ID
}

// postIngest sends one seeding request and returns the recorder.
func postIngest(h *monitoringTestIngestHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/monitoring/test-ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ingest(rec, req)
	return rec
}

// backdatedBody builds `days` daily lines at 09:00 UTC, oldest first.
func backdatedBody(sourceID string, days int, now time.Time) (string, []time.Time) {
	stamps := make([]time.Time, 0, days)
	lines := make([]string, 0, days)
	for i := days; i >= 1; i-- {
		day := now.AddDate(0, 0, -i)
		ts := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC)
		stamps = append(stamps, ts)
		lines = append(lines, fmt.Sprintf(`{"ts":%q,"message":%q,"stream":"stdout"}`,
			ts.Format(time.RFC3339), testIngestMessage))
	}
	body := fmt.Sprintf(`{"workspace_id":"ws-A","source_id":%q,"lines":[%s]}`,
		sourceID, strings.Join(lines, ","))
	return body, stamps
}

// TestTestIngestGateClosedByDefault — the route exists but must not work
// without the opt-in, and must say not-found rather than forbidden.
func TestTestIngestGateClosedByDefault(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_TEST_INGEST", "")
	h, _, srcA, _ := newTestIngestFixture(t, 30)
	body, _ := backdatedBody(srcA, 1, time.Now().UTC())
	rec := postIngest(h, body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("gated status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	tick := &monitoringTickHandler{}
	rec = httptest.NewRecorder()
	tick.tick(rec, httptest.NewRequest(http.MethodPost, "/api/v1/monitoring/test-tick", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("gated tick status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

// TestTestIngestHonoursBackdatedTimestamps is the requirement everything else
// rests on: seven days of history in one call must land on seven distinct days,
// with the timestamps stored exactly as submitted, through the real distiller.
func TestTestIngestHonoursBackdatedTimestamps(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_TEST_INGEST", "1")
	h, db, srcA, _ := newTestIngestFixture(t, 30)
	ctx := context.Background()
	now := time.Now().UTC()
	body, stamps := backdatedBody(srcA, 7, now)

	rec := postIngest(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Ingested            int `json:"ingested"`
		DaysSpanned         int `json:"days_spanned"`
		LinesBelowRetention int `json:"lines_below_retention"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Ingested != 7 || out.DaysSpanned != 7 || out.LinesBelowRetention != 0 {
		t.Fatalf("result = %+v, want 7 ingested / 7 days / 0 below retention", out)
	}

	// The distiller — not this endpoint — decides the template identity, so
	// finding the row under distill.TemplateID proves the real path ran.
	templateID := distill.TemplateID(srcA, distill.Normalize(testIngestMessage))
	tpl, err := db.GetLogTemplate(ctx, templateID)
	if err != nil {
		t.Fatalf("template not created by the distill path: %v", err)
	}
	if tpl.Count != 7 {
		t.Errorf("template count = %d, want 7", tpl.Count)
	}
	if !tpl.FirstSeen.Equal(stamps[0]) {
		t.Errorf("template first_seen = %s, want the oldest submitted ts %s",
			tpl.FirstSeen, stamps[0])
	}
	if !tpl.LastSeen.Equal(stamps[len(stamps)-1]) {
		t.Errorf("template last_seen = %s, want the newest submitted ts %s",
			tpl.LastSeen, stamps[len(stamps)-1])
	}

	// Raw lines must carry the submitted instants verbatim, not the clock.
	lines, err := db.ListLogLinesByTemplate(ctx, templateID, 100)
	if err != nil {
		t.Fatalf("list lines: %v", err)
	}
	if len(lines) != 7 {
		t.Fatalf("retained lines = %d, want 7", len(lines))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		seen[line.TS.UTC().Format(time.RFC3339)] = true
	}
	for _, ts := range stamps {
		if !seen[ts.Format(time.RFC3339)] {
			t.Errorf("no retained line at %s — backdating was not honoured",
				ts.Format(time.RFC3339))
		}
	}

	// log_template_days is written by an insert trigger keyed on the line's own
	// ts. This is the assertion that the day history landed on the RIGHT days
	// rather than seven rows on today.
	history, err := db.GetLogTemplateHistory(ctx, templateID)
	if err != nil {
		t.Fatalf("template history: %v", err)
	}
	if history.ObservedDistinctDays != 7 {
		t.Errorf("observed_distinct_days = %d, want 7", history.ObservedDistinctDays)
	}
	wantFirst := stamps[0].Format("2006-01-02")
	wantLast := stamps[len(stamps)-1].Format("2006-01-02")
	if got := history.ObservedFirstDay.Format("2006-01-02"); got != wantFirst {
		t.Errorf("observed_first_day = %s, want %s", got, wantFirst)
	}
	if got := history.ObservedLastDay.Format("2006-01-02"); got != wantLast {
		t.Errorf("observed_last_day = %s, want %s", got, wantLast)
	}
}

// TestTestIngestReportsRetentionPruning — Distiller.Ingest prunes below
// now-RetentionDays in the same call that writes the lines, so a scenario
// seeding past its source's retention gets fewer retained lines than it sent.
// The response must say so, and the day history must survive regardless.
func TestTestIngestReportsRetentionPruning(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_TEST_INGEST", "1")
	h, db, srcA, _ := newTestIngestFixture(t, 2)
	ctx := context.Background()
	body, stamps := backdatedBody(srcA, 5, time.Now().UTC())

	rec := postIngest(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		Ingested            int `json:"ingested"`
		LinesBelowRetention int `json:"lines_below_retention"`
		RetentionDays       int `json:"retention_days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RetentionDays != 2 {
		t.Fatalf("retention_days = %d, want 2", out.RetentionDays)
	}
	if out.LinesBelowRetention == 0 {
		t.Fatal("lines_below_retention = 0 — a caller seeding past retention was not warned")
	}

	templateID := distill.TemplateID(srcA, distill.Normalize(testIngestMessage))
	lines, err := db.ListLogLinesByTemplate(ctx, templateID, 100)
	if err != nil {
		t.Fatalf("list lines: %v", err)
	}
	if len(lines) >= len(stamps) {
		t.Errorf("retained %d of %d lines — expected pruning below a 2-day retention",
			len(lines), len(stamps))
	}
	// Day history is not pruned, so the cadence evidence survives the lines.
	history, err := db.GetLogTemplateHistory(ctx, templateID)
	if err != nil {
		t.Fatalf("template history: %v", err)
	}
	if history.ObservedDistinctDays != 5 {
		t.Errorf("observed_distinct_days = %d, want 5 (day history must outlive pruned lines)",
			history.ObservedDistinctDays)
	}
}

// TestTestIngestRejectsForeignSourceAndBadInput covers the ownership gate and
// the input bounds in one pass.
func TestTestIngestRejectsForeignSourceAndBadInput(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_TEST_INGEST", "1")
	h, _, srcA, srcB := newTestIngestFixture(t, 30)
	now := time.Now().UTC()

	// ws-A must not be able to seed ws-B's source, and the refusal must look
	// exactly like a missing source rather than announcing one exists.
	foreign, _ := backdatedBody(srcB, 1, now)
	if rec := postIngest(h, foreign); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}

	future := now.Add(4 * time.Hour).Format(time.RFC3339)
	cases := map[string]string{
		"future ts": fmt.Sprintf(`{"workspace_id":"ws-A","source_id":%q,`+
			`"lines":[{"ts":%q,"message":"x"}]}`, srcA, future),
		"bad ts": fmt.Sprintf(`{"workspace_id":"ws-A","source_id":%q,`+
			`"lines":[{"ts":"2026-07-13 09:00","message":"x"}]}`, srcA),
		"empty message": fmt.Sprintf(`{"workspace_id":"ws-A","source_id":%q,`+
			`"lines":[{"ts":%q,"message":""}]}`, srcA, now.Format(time.RFC3339)),
		"no lines":     fmt.Sprintf(`{"workspace_id":"ws-A","source_id":%q,"lines":[]}`, srcA),
		"no source_id": `{"workspace_id":"ws-A","lines":[{"message":"x"}]}`,
	}
	for name, body := range cases {
		if rec := postIngest(h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", name, rec.Code, rec.Body)
		}
	}
}
