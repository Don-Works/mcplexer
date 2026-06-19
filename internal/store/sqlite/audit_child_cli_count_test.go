package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestCountChildCLIToolCalls drives the SQL filter end-to-end: writes a
// mix of audit_records and asserts the count is the intersection of
// (workspace, time-window, child client_type, NOT worker actor_kind,
// status=success). The fix relies on this filter being narrow enough
// to not overcount unrelated user / runner activity in the same window.
func TestCountChildCLIToolCalls(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	windowStart := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(60 * time.Second)

	type seedRow struct {
		ts          time.Time
		workspaceID string
		clientType  string
		actorKind   string
		status      string
		toolName    string
	}

	rows := []seedRow{
		// counts (5 rows in-window, ws-a, child CLI client_type, success, non-worker actor)
		{ts: windowStart.Add(5 * time.Second), workspaceID: "ws-a", clientType: "claude_cli", actorKind: "user", status: "success", toolName: "github__list_issues"},
		{ts: windowStart.Add(15 * time.Second), workspaceID: "ws-a", clientType: "opencode", actorKind: "user", status: "success", toolName: "fetch__get"},
		{ts: windowStart.Add(45 * time.Second), workspaceID: "ws-a", clientType: "claude-code", actorKind: "user", status: "success", toolName: "memory__save"},
		{ts: windowStart.Add(50 * time.Second), workspaceID: "ws-a", clientType: "grok_cli", actorKind: "user", status: "success", toolName: "mesh__send"},
		{ts: windowStart.Add(55 * time.Second), workspaceID: "ws-a", clientType: "mimocode", actorKind: "user", status: "success", toolName: "task__create"},

		// does NOT count: out-of-window (before)
		{ts: windowStart.Add(-1 * time.Second), workspaceID: "ws-a", clientType: "claude_cli", actorKind: "user", status: "success", toolName: "github__list_issues"},
		// does NOT count: out-of-window (after)
		{ts: windowEnd.Add(1 * time.Second), workspaceID: "ws-a", clientType: "claude_cli", actorKind: "user", status: "success", toolName: "github__list_issues"},
		// does NOT count: different workspace
		{ts: windowStart.Add(10 * time.Second), workspaceID: "ws-b", clientType: "claude_cli", actorKind: "user", status: "success", toolName: "github__list_issues"},
		// does NOT count: runner-emitted worker_run.started
		{ts: windowStart.Add(20 * time.Second), workspaceID: "ws-a", clientType: "worker", actorKind: "worker", status: "ok", toolName: "worker_run.started"},
		// does NOT count: unrecognised client_type (e.g. an unrelated mcp client)
		{ts: windowStart.Add(25 * time.Second), workspaceID: "ws-a", clientType: "vscode", actorKind: "user", status: "success", toolName: "fetch__get"},
		// does NOT count: status != success (denied tool call)
		{ts: windowStart.Add(30 * time.Second), workspaceID: "ws-a", clientType: "claude_cli", actorKind: "user", status: "blocked", toolName: "github__list_issues"},
	}

	for i, r := range rows {
		rec := &store.AuditRecord{
			Timestamp:   r.ts,
			CreatedAt:   r.ts,
			SessionID:   "sess-" + r.clientType,
			ClientType:  r.clientType,
			WorkspaceID: r.workspaceID,
			ToolName:    r.toolName,
			Status:      r.status,
			ActorKind:   r.actorKind,
		}
		if err := db.InsertAuditRecord(ctx, rec); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	clientTypes := []string{
		"claude_cli", "claude_code", "claude-code",
		"opencode", "opencode_cli",
		"grok", "grok_cli", "xai", "xai_cli",
		"mimo", "mimo_cli", "mimocode",
	}
	got, err := db.CountChildCLIToolCalls(ctx, "ws-a", windowStart, windowEnd, clientTypes)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 5 {
		t.Fatalf("count = %d, want 5 (in-window + ws-a + child client + non-worker + success)", got)
	}

	// Sanity: a tighter window catches only the first row.
	tighter := windowStart.Add(10 * time.Second)
	got, err = db.CountChildCLIToolCalls(ctx, "ws-a", windowStart, tighter, clientTypes)
	if err != nil {
		t.Fatalf("count tighter: %v", err)
	}
	if got != 1 {
		t.Fatalf("count tighter = %d, want 1", got)
	}

	// Empty workspace_id and empty clientTypes both short-circuit to 0
	// without touching the DB (guards the IN-clause + denormalised
	// workspace_id semantics).
	if got, err := db.CountChildCLIToolCalls(ctx, "", windowStart, windowEnd, clientTypes); err != nil || got != 0 {
		t.Fatalf("empty workspace = (%d, %v), want (0, nil)", got, err)
	}
	if got, err := db.CountChildCLIToolCalls(ctx, "ws-a", windowStart, windowEnd, nil); err != nil || got != 0 {
		t.Fatalf("empty client_types = (%d, %v), want (0, nil)", got, err)
	}
}
