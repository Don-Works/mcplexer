package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedAuditSearchRows inserts a small, deterministic corpus across distinct
// tools / error messages / workspaces so the FTS + TF-IDF assertions have
// something unambiguous to rank.
func seedAuditSearchRows(t *testing.T, db interface {
	InsertAuditRecord(context.Context, *store.AuditRecord) error
}) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	rows := []store.AuditRecord{
		{ToolName: "github__create_issue", Status: "success", WorkspaceName: "acme",
			Subpath: "issues", ParamsRedacted: json.RawMessage(`{"title":"deploy failure"}`)},
		{ToolName: "github__list_prs", Status: "success", WorkspaceName: "acme",
			Subpath: "pulls", ParamsRedacted: json.RawMessage(`{"state":"open"}`)},
		{ToolName: "slack__post_message", Status: "error", WorkspaceName: "beta",
			ErrorMessage:   "rate limit exceeded on deploy channel",
			ParamsRedacted: json.RawMessage(`{"channel":"deploy"}`)},
		{ToolName: "postgres__query", Status: "error", WorkspaceName: "beta",
			ErrorMessage: "connection refused", ParamsRedacted: json.RawMessage(`{}`)},
	}
	for i := range rows {
		rows[i].Timestamp = base.Add(time.Duration(i) * time.Minute)
		rows[i].CreatedAt = rows[i].Timestamp
		rows[i].WorkspaceID = "ws-" + rows[i].WorkspaceName
		if err := db.InsertAuditRecord(ctx, &rows[i]); err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
	}
}

// TestSearchAuditRecordsFTSMatch confirms the FTS index narrows the
// candidate pool to lexically-matching rows before TF-IDF ranking.
func TestSearchAuditRecordsFTSMatch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedAuditSearchRows(t, db)

	recs, mode, err := db.SearchAuditRecords(ctx, store.AuditFilter{Q: "deploy"}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if mode != "tfidf" {
		t.Fatalf("mode = %q, want tfidf", mode)
	}
	// "deploy" appears in the github__create_issue params and the slack
	// error message — both must be returned, the postgres + list_prs rows
	// must not.
	got := map[string]bool{}
	for _, r := range recs {
		got[r.ToolName] = true
	}
	if !got["github__create_issue"] || !got["slack__post_message"] {
		t.Fatalf("expected deploy matches, got %v", got)
	}
	if got["postgres__query"] {
		t.Fatalf("postgres row should not match 'deploy': %v", got)
	}
}

// TestSearchAuditRecordsSanitizeSafety feeds FTS5 metacharacters and a
// SQL-injection-shaped string; the query must not error and must return a
// clean (possibly empty) result rather than blowing up the MATCH.
func TestSearchAuditRecordsSanitizeSafety(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedAuditSearchRows(t, db)

	for _, q := range []string{
		`deploy" OR 1=1; DROP TABLE audit_records;--`,
		`*:()[]-+`,
		`"unterminated`,
		`connection AND refused`,
	} {
		recs, _, err := db.SearchAuditRecords(ctx, store.AuditFilter{Q: q}, 10)
		if err != nil {
			t.Fatalf("search %q errored: %v", q, err)
		}
		_ = recs // result content is not asserted; the point is no panic/error
	}

	// The table must still exist + be queryable after the injection-shaped
	// query (proves the MATCH was parameterised, not concatenated).
	_, total, err := db.QueryAuditRecords(ctx, store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("post-injection query: %v", err)
	}
	if total != 4 {
		t.Fatalf("post-injection total = %d, want 4", total)
	}
}

// TestSearchAuditRecordsEmptyQueryFallsBackToRecency confirms an empty /
// term-free query returns recency-ordered rows with mode "fts".
func TestSearchAuditRecordsEmptyQuery(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedAuditSearchRows(t, db)

	recs, mode, err := db.SearchAuditRecords(ctx, store.AuditFilter{Q: "  "}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if mode != "fts" {
		t.Fatalf("mode = %q, want fts", mode)
	}
	if len(recs) != 4 {
		t.Fatalf("recency fallback returned %d rows, want 4", len(recs))
	}
	// Recency order = newest first (postgres was inserted last).
	if recs[0].ToolName != "postgres__query" {
		t.Fatalf("first row = %q, want postgres__query (newest)", recs[0].ToolName)
	}
}

// TestSearchAuditRecordsScopedByFilter confirms exact-match filters narrow
// the pool before search (here: workspace scope).
func TestSearchAuditRecordsScopedByFilter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedAuditSearchRows(t, db)

	ws := "ws-beta"
	recs, _, err := db.SearchAuditRecords(ctx,
		store.AuditFilter{Q: "deploy", WorkspaceID: &ws}, 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, r := range recs {
		if r.WorkspaceID != "ws-beta" {
			t.Fatalf("row out of workspace scope: %+v", r.WorkspaceID)
		}
	}
	// github__create_issue (ws-acme) also matches "deploy" but must be
	// excluded by the workspace filter.
	for _, r := range recs {
		if r.ToolName == "github__create_issue" {
			t.Fatalf("acme row leaked into beta-scoped search")
		}
	}
}
