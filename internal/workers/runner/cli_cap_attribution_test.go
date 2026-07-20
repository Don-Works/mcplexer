package runner

// Regression cover for CLI tool-call cap contamination.
//
// The audit-derived cap used to count every CLI-family audit row in a
// (workspace, time-window) with no run correlation at all. Two things fell
// out of that, both observed live:
//
//   - The operator's own Claude Code session announces client_type
//     "claude-code", which is also what a claude_cli child announces, so the
//     parent orchestrator's tool calls were billed to its own workers.
//   - Concurrent delegations in one workspace all saw the same total and
//     rose in lockstep, promoting healthy runs to cap_exceeded.
//
// These tests drive the real sqlite store (not a stub) so the SQL filter and
// the attribution policy are exercised together — the bug lived in the seam
// between them.

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/google/uuid"
)

// Compile-time proof that the sqlite store still satisfies the session
// attribution interface. applyCLIToolCallCap reaches it by type assertion
// (the wiring passes a store.Store), so without this the store could drop
// the method and the cap would silently stop firing forever.
var _ CLISessionToolCallCounter = (*sqlite.DB)(nil)

func newAttributionDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/attribution.db")
	if err != nil {
		t.Fatalf("new test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedSession creates an MCP session row and n successful audit_records
// attributed to it. Returns the session id.
func seedSession(
	t *testing.T, db *sqlite.DB, workspaceID, clientType string,
	connectedAt time.Time, disconnectedAt *time.Time, calls int, callsAt time.Time,
) string {
	t.Helper()
	ctx := context.Background()
	sess := &store.Session{
		ID:             uuid.NewString(),
		ClientType:     clientType,
		ConnectedAt:    connectedAt,
		DisconnectedAt: disconnectedAt,
	}
	if err := db.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < calls; i++ {
		rec := &store.AuditRecord{
			ID:          uuid.NewString(),
			Timestamp:   callsAt,
			CreatedAt:   callsAt,
			SessionID:   sess.ID,
			ClientType:  clientType,
			WorkspaceID: workspaceID,
			ToolName:    "github__list_issues",
			Status:      "success",
			ActorKind:   "user",
		}
		if err := db.InsertAuditRecord(ctx, rec); err != nil {
			t.Fatalf("insert audit record %d: %v", i, err)
		}
	}
	return sess.ID
}

func ptrTime(t time.Time) *time.Time { return &t }

// applyCap runs the post-run cap check on a fresh success outcome and
// returns the three things these tests care about: the terminal status, the
// error annotation, and the tool-call count stamped on the loop state.
func applyCap(r *Runner, worker *store.Worker, run *store.WorkerRun) (string, string, int) {
	state := &loopState{}
	outcome := loopOutcome{status: StatusSuccess}
	r.applyCLIToolCallCap(context.Background(), worker, run, state, &outcome)
	return outcome.status, outcome.errorText, state.toolCallCount
}

// TestApplyCLIToolCallCap_IgnoresParentOrchestratorSession is the proven
// bug: a pi_cli worker that called three tools was promoted to cap_exceeded
// because the human's Claude Code session made 100 calls in the same window.
func TestApplyCLIToolCallCap_IgnoresParentOrchestratorSession(t *testing.T) {
	db := newAttributionDB(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	// The operator's orchestrator session: connected two hours before the
	// run, still going, 100 tool calls inside the run window.
	seedSession(t, db, "ws-1", "claude-code",
		base.Add(-2*time.Hour), nil, 100, base.Add(10*time.Second))
	// The worker's own pi child: opened during the run, three calls.
	seedSession(t, db, "ws-1", "pi",
		base.Add(1*time.Second), ptrTime(base.Add(60*time.Second)),
		3, base.Add(20*time.Second))

	r := New(Deps{CLIToolCounter: db})
	worker := &store.Worker{ModelProvider: "pi_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: base}

	status, errText, count := applyCap(r, worker, run)

	if status != StatusSuccess {
		t.Fatalf("status = %q (%s), want success — the parent session's calls are not this worker's",
			status, errText)
	}
	if count != 3 {
		t.Fatalf("toolCallCount = %d, want 3 (the child session's own calls)", count)
	}
}

// TestApplyCLIToolCallCap_ConcurrentSameFamilyRunsAreNotAttributed covers
// the lockstep-counts observation. Two pi_cli runs overlap in one workspace,
// so neither run's audit rows can be told from the other's. The cap must not
// fire on either — under-enforcing costs a few tool calls, misattributing
// manufactures a failure on a worker that did nothing wrong.
func TestApplyCLIToolCallCap_ConcurrentSameFamilyRunsAreNotAttributed(t *testing.T) {
	db := newAttributionDB(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	// Run A's child: three calls.
	seedSession(t, db, "ws-1", "pi",
		base.Add(1*time.Second), ptrTime(base.Add(50*time.Second)),
		3, base.Add(20*time.Second))
	// Run B's child, overlapping A: ninety calls.
	seedSession(t, db, "ws-1", "pi",
		base.Add(6*time.Second), ptrTime(base.Add(55*time.Second)),
		90, base.Add(30*time.Second))

	r := New(Deps{CLIToolCounter: db})
	worker := &store.Worker{ModelProvider: "pi_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: base}

	status, errText, count := applyCap(r, worker, run)

	if status != StatusSuccess {
		t.Fatalf("status = %q (%s), want success — overlapping same-family sessions are unattributable",
			status, errText)
	}
	if count != 0 {
		t.Fatalf("toolCallCount = %d, want 0 — an unattributable run has no honest count", count)
	}
}

// TestApplyCLIToolCallCap_DifferentFamiliesStayAttributed is the other half:
// narrowing by provider family means concurrent runs on different CLIs are
// still counted, and a run that genuinely blows its cap still fails.
func TestApplyCLIToolCallCap_DifferentFamiliesStayAttributed(t *testing.T) {
	db := newAttributionDB(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	seedSession(t, db, "ws-1", "pi",
		base.Add(1*time.Second), ptrTime(base.Add(50*time.Second)),
		3, base.Add(20*time.Second))
	seedSession(t, db, "ws-1", "claude-code",
		base.Add(6*time.Second), ptrTime(base.Add(55*time.Second)),
		90, base.Add(30*time.Second))

	r := New(Deps{CLIToolCounter: db})
	run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: base}

	piWorker := &store.Worker{ModelProvider: "pi_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	status, errText, count := applyCap(r, piWorker, run)
	if status != StatusSuccess {
		t.Fatalf("pi status = %q (%s), want success", status, errText)
	}
	if count != 3 {
		t.Fatalf("pi toolCallCount = %d, want 3", count)
	}

	claudeWorker := &store.Worker{ModelProvider: "claude_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	status, errText, count = applyCap(r, claudeWorker, run)
	if status != StatusCapExceeded {
		t.Fatalf("claude status = %q, want cap_exceeded — 90 > 80 is a real breach", status)
	}
	if count != 90 {
		t.Fatalf("claude toolCallCount = %d, want 90", count)
	}
	if errText == "" {
		t.Fatal("cap_exceeded must explain the breached cap")
	}
}

// TestApplyCLIToolCallCap_SequentialChildSessionsSum covers the claude_cli
// shape: one run spawns a fresh `claude` per Send, so a single run legitimately
// owns several child sessions. They never overlap, so they sum.
func TestApplyCLIToolCallCap_SequentialChildSessionsSum(t *testing.T) {
	db := newAttributionDB(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	seedSession(t, db, "ws-1", "claude-code",
		base.Add(1*time.Second), ptrTime(base.Add(30*time.Second)),
		50, base.Add(10*time.Second))
	seedSession(t, db, "ws-1", "claude-code",
		base.Add(40*time.Second), ptrTime(base.Add(70*time.Second)),
		45, base.Add(50*time.Second))

	r := New(Deps{CLIToolCounter: db})
	worker := &store.Worker{ModelProvider: "claude_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: base}

	status, _, count := applyCap(r, worker, run)

	if count != 95 {
		t.Fatalf("toolCallCount = %d, want 95 (50 + 45 across sequential child sessions)", count)
	}
	if status != StatusCapExceeded {
		t.Fatalf("status = %q, want cap_exceeded", status)
	}
}

// TestApplyCLIToolCallCap_OtherWorkspaceIsInvisible keeps the pre-existing
// workspace scoping honest now that the query grew a join.
func TestApplyCLIToolCallCap_OtherWorkspaceIsInvisible(t *testing.T) {
	db := newAttributionDB(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)

	seedSession(t, db, "ws-other", "pi",
		base.Add(1*time.Second), ptrTime(base.Add(50*time.Second)),
		200, base.Add(20*time.Second))

	r := New(Deps{CLIToolCounter: db})
	worker := &store.Worker{ModelProvider: "pi_cli", MaxToolCalls: 80, WorkspaceID: "ws-1"}
	run := &store.WorkerRun{WorkspaceID: "ws-1", StartedAt: base}

	status, _, count := applyCap(r, worker, run)
	if status != StatusSuccess || count != 0 {
		t.Fatalf("status = %q, count = %d, want (success, 0)", status, count)
	}
}

func TestAttributeCLIToolCalls(t *testing.T) {
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	windowEnd := base.Add(5 * time.Minute)
	session := func(connect, disconnect int, count int) store.ChildCLISessionCount {
		s := store.ChildCLISessionCount{
			ConnectedAt: base.Add(time.Duration(connect) * time.Second),
			Count:       count,
		}
		if disconnect >= 0 {
			s.DisconnectedAt = ptrTime(base.Add(time.Duration(disconnect) * time.Second))
		}
		return s
	}

	for _, tc := range []struct {
		name           string
		sessions       []store.ChildCLISessionCount
		wantCount      int
		wantAttributed bool
	}{
		{name: "no sessions"},
		{
			name:           "single session",
			sessions:       []store.ChildCLISessionCount{session(1, 10, 7)},
			wantCount:      7,
			wantAttributed: true,
		},
		{
			name:           "single still-open session",
			sessions:       []store.ChildCLISessionCount{session(1, -1, 7)},
			wantCount:      7,
			wantAttributed: true,
		},
		{
			name:           "sequential sessions sum",
			sessions:       []store.ChildCLISessionCount{session(1, 10, 7), session(20, 30, 5)},
			wantCount:      12,
			wantAttributed: true,
		},
		{
			name:     "overlapping sessions are ambiguous",
			sessions: []store.ChildCLISessionCount{session(1, 25, 7), session(20, 30, 5)},
		},
		{
			name:     "an open session swallows everything after it",
			sessions: []store.ChildCLISessionCount{session(1, -1, 7), session(20, 30, 5)},
		},
		{
			// Two clean sequential sessions followed by an overlapping
			// third: one bad pair poisons the whole set.
			name: "overlap anywhere in the sequence is ambiguous",
			sessions: []store.ChildCLISessionCount{
				session(1, 10, 1), session(20, 40, 2), session(35, 50, 3),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, attributed := AttributeCLIToolCalls(tc.sessions, windowEnd)
			if count != tc.wantCount || attributed != tc.wantAttributed {
				t.Fatalf("= (%d, %v), want (%d, %v)",
					count, attributed, tc.wantCount, tc.wantAttributed)
			}
		})
	}
}
