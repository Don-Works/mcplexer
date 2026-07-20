package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestCountChildCLIToolCallsBySession drives the session-attributed count.
// The flat CountChildCLIToolCalls returns one number for a (workspace,
// window) with no way to tell whose calls those were; this one splits the
// same rows per MCP session and drops any session that cannot belong to the
// run — which is what stops a parent orchestrator's tool calls being counted
// against the workers it spawned.
func TestCountChildCLIToolCallsBySession(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	windowStart := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(120 * time.Second)

	seedSession := func(id, clientType string, connected time.Time, disconnected *time.Time) {
		t.Helper()
		if err := db.CreateSession(ctx, &store.Session{
			ID: id, ClientType: clientType,
			ConnectedAt: connected, DisconnectedAt: disconnected,
		}); err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
	}
	seedRow := func(sessionID, clientType, workspaceID, status string, ts time.Time) {
		t.Helper()
		if err := db.InsertAuditRecord(ctx, &store.AuditRecord{
			Timestamp: ts, CreatedAt: ts,
			SessionID: sessionID, ClientType: clientType,
			WorkspaceID: workspaceID, ToolName: "github__list_issues",
			Status: status, ActorKind: "user",
		}); err != nil {
			t.Fatalf("insert audit row for %s: %v", sessionID, err)
		}
	}

	// The operator's own orchestrator: same client_type a claude_cli child
	// announces, but connected long before the window opened.
	parentDisconnect := windowEnd.Add(time.Hour)
	seedSession("sess-parent", "claude-code", windowStart.Add(-2*time.Hour), &parentDisconnect)
	for i := 0; i < 4; i++ {
		seedRow("sess-parent", "claude-code", "ws-a", "success", windowStart.Add(10*time.Second))
	}

	// Two CLI children that opened during the window.
	childADisconnect := windowStart.Add(40 * time.Second)
	seedSession("sess-child-a", "claude-code", windowStart.Add(5*time.Second), &childADisconnect)
	for i := 0; i < 3; i++ {
		seedRow("sess-child-a", "claude-code", "ws-a", "success", windowStart.Add(20*time.Second))
	}
	seedSession("sess-child-b", "claude-code", windowStart.Add(50*time.Second), nil) // still open
	for i := 0; i < 2; i++ {
		seedRow("sess-child-b", "claude-code", "ws-a", "success", windowStart.Add(60*time.Second))
	}

	// Excluded for reasons the flat count already applied.
	seedSession("sess-other-ws", "claude-code", windowStart.Add(5*time.Second), nil)
	seedRow("sess-other-ws", "claude-code", "ws-b", "success", windowStart.Add(20*time.Second))
	seedRow("sess-child-a", "claude-code", "ws-a", "blocked", windowStart.Add(21*time.Second))
	seedRow("sess-child-a", "claude-code", "ws-a", "success", windowEnd.Add(time.Second))

	// An audit row whose session row does not exist cannot be attributed to
	// anything, so the inner join drops it rather than folding it into some
	// run's total.
	seedRow("sess-vanished", "claude-code", "ws-a", "success", windowStart.Add(30*time.Second))

	clientTypes := []string{"claude_cli", "claude_code", "claude-code"}
	got, err := db.CountChildCLIToolCallsBySession(ctx, "ws-a", windowStart, windowEnd, clientTypes)
	if err != nil {
		t.Fatalf("count by session: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("sessions = %+v, want exactly the two children that connected in-window", got)
	}
	// Ordered by connected_at ascending — callers rely on this to reason
	// about session overlap.
	if got[0].SessionID != "sess-child-a" || got[1].SessionID != "sess-child-b" {
		t.Fatalf("order = %q, %q; want sess-child-a then sess-child-b",
			got[0].SessionID, got[1].SessionID)
	}
	if got[0].Count != 3 {
		t.Fatalf("child-a count = %d, want 3 (blocked + out-of-window excluded)", got[0].Count)
	}
	if got[1].Count != 2 {
		t.Fatalf("child-b count = %d, want 2", got[1].Count)
	}
	if got[0].DisconnectedAt == nil || !got[0].DisconnectedAt.Equal(childADisconnect) {
		t.Fatalf("child-a disconnected_at = %v, want %v", got[0].DisconnectedAt, childADisconnect)
	}
	if got[1].DisconnectedAt != nil {
		t.Fatalf("child-b disconnected_at = %v, want nil (still open)", got[1].DisconnectedAt)
	}
	if !got[0].ConnectedAt.Equal(windowStart.Add(5 * time.Second)) {
		t.Fatalf("child-a connected_at = %v", got[0].ConnectedAt)
	}
	if got[0].ClientType != "claude-code" {
		t.Fatalf("child-a client_type = %q, want claude-code", got[0].ClientType)
	}

	// The flat count over the same rows is 9 — the 4 parent calls, the
	// vanished session's row, and both children summed together. That gap
	// is the contamination this method exists to remove.
	flat, err := db.CountChildCLIToolCalls(ctx, "ws-a", windowStart, windowEnd, clientTypes)
	if err != nil {
		t.Fatalf("flat count: %v", err)
	}
	if flat != 10 {
		t.Fatalf("flat count = %d, want 10 (parent 4 + child-a 3 + child-b 2 + vanished 1)", flat)
	}

	// Empty workspace / client types short-circuit, matching the flat count.
	if got, err := db.CountChildCLIToolCallsBySession(ctx, "", windowStart, windowEnd, clientTypes); err != nil || got != nil {
		t.Fatalf("empty workspace = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := db.CountChildCLIToolCallsBySession(ctx, "ws-a", windowStart, windowEnd, nil); err != nil || got != nil {
		t.Fatalf("empty client_types = (%v, %v), want (nil, nil)", got, err)
	}
}
