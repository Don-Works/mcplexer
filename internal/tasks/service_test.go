// service_test.go — focused coverage of the tasks service layer's
// non-trivial behaviour: claim refuses races, compose_into stamps the
// bidirectional link (composed_by on child + composes on parent),
// status_history append is monotonic.
package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

func newSvc(t *testing.T) (*tasks.Service, *sqlite.DB, string) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w := &store.Workspace{Name: "ws1", RootPath: "/tmp/ws1", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return tasks.New(d), d, w.ID
}

func TestCreateAppendsStatusHistoryEntry(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	got, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              "First task",
		Status:             "open",
		CreatedBySessionID: "max-session",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var history []store.TaskStatusHistoryEntry
	if err := json.Unmarshal(got.StatusHistoryJSON, &history); err != nil {
		t.Fatalf("history json: %v", err)
	}
	if len(history) != 1 || history[0].Evt != "created" {
		t.Fatalf("expected single created entry, got %+v", history)
	}
}

func TestUpdateStatusAppendsHistory(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Build the thing", CreatedBySessionID: "agent-a",
	})
	newStatus := "doing"
	t2, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status:             &newStatus,
		UpdatedBySessionID: "agent-a",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(t2.StatusHistoryJSON, &history)
	// 3 entries: created, status_changed, auto-assigned (auto-claim on
	// status=doing when the row had no assignee).
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries (incl. auto-claim), got %d: %+v", len(history), history)
	}
	if history[1].Evt != "status_changed" || history[1].From != "open" || history[1].To != "doing" {
		t.Fatalf("unexpected status_changed entry: %+v", history[1])
	}
	if history[2].Evt != "assigned" || history[2].To != "agent-a" {
		t.Fatalf("expected auto-claim assigned entry, got: %+v", history[2])
	}
	if t2.AssigneeSessionID != "agent-a" {
		t.Fatalf("expected auto-claim to assignee=max, got %q", t2.AssigneeSessionID)
	}
}

// TestUpdateStatusKindWorkingTriggersAutoClaim — migration 070 added
// `kind` to task_status_vocabulary. The service-layer auto-claim path
// must now treat ANY status with kind="working" as equivalent to the
// hardcoded "doing" fallback. This covers the freeform-vocabulary
// generalization: an agent that declares `triaging → working` should
// see triaging-status transitions auto-claim the same way doing does.
func TestUpdateStatusKindWorkingTriggersAutoClaim(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	// Declare "triaging" as a working-kind status in this workspace.
	if err := db.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "triaging", Kind: "working",
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab: %v", err)
	}

	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "needs triage", CreatedBySessionID: "agent-a",
	})
	if t1.AssigneeSessionID != "" {
		t.Fatalf("precondition: expected unassigned, got %q", t1.AssigneeSessionID)
	}
	newStatus := "triaging"
	t2, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status:             &newStatus,
		UpdatedBySessionID: "agent-a",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if t2.AssigneeSessionID != "agent-a" {
		t.Fatalf("expected auto-claim on kind=working status; assignee=%q want max", t2.AssigneeSessionID)
	}
	// History must include the assigned entry with the auto-claim note —
	// same shape as the literal-doing path.
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(t2.StatusHistoryJSON, &history)
	if len(history) < 3 {
		t.Fatalf("expected ≥3 history entries (created+status_changed+assigned), got %d: %+v", len(history), history)
	}
	last := history[len(history)-1]
	if last.Evt != "assigned" || last.To != "agent-a" {
		t.Fatalf("expected last entry to be auto-claim assigned: %+v", last)
	}
}

// TestUpdateStatusKindOpenDoesNotAutoClaim — a status word declared
// with kind="open" (or anything other than working) must NOT trigger
// auto-claim, even if the literal text happens to be "doing". The
// vocab classification wins over the hardcoded fallback.
func TestUpdateStatusKindOpenDoesNotAutoClaim(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	// Edge case: workspace declares "doing" as kind=blocked (weird but
	// legal — maybe the workspace coined `doing` to mean "waiting on a
	// decision"). Auto-claim must respect the declared classification.
	if err := db.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "doing", Kind: "blocked",
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab: %v", err)
	}
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "ambiguous", CreatedBySessionID: "agent-a",
	})
	newStatus := "doing"
	t2, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status:             &newStatus,
		UpdatedBySessionID: "agent-a",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if t2.AssigneeSessionID != "" {
		t.Fatalf("expected NO auto-claim when vocab says kind=blocked; assignee=%q", t2.AssigneeSessionID)
	}
}

// TestUpdateStatusDoingPreservesExistingAssignee — auto-claim must NOT
// steal a row already owned. Only no-assignee → doing triggers the
// auto-claim path; an explicit Assignee patch still wins.
func TestUpdateStatusDoingPreservesExistingAssignee(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "owned-already", CreatedBySessionID: "alice",
		Assignee: &tasks.Assignee{SessionID: "alice"},
	})
	newStatus := "doing"
	t2, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status: &newStatus, UpdatedBySessionID: "bob",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if t2.AssigneeSessionID != "alice" {
		t.Fatalf("auto-claim stole an owned row: assignee=%q want alice", t2.AssigneeSessionID)
	}
}

func TestClaimAssignsToMeAndSetsStatus(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Up for grabs", CreatedBySessionID: "system",
	})
	claimed, err := svc.Claim(ctx, wsID, t1.ID, "", "max-session", "")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.AssigneeSessionID != "max-session" {
		t.Fatalf("expected assignee=max-session, got %q", claimed.AssigneeSessionID)
	}
	if claimed.Status != "doing" {
		t.Fatalf("expected status=doing (default), got %q", claimed.Status)
	}
}

func TestClaimRefusesRace(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Single owner",
	})
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "first", ""); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, err := svc.Claim(ctx, wsID, t1.ID, "", "second", "")
	if !errors.Is(err, tasks.ErrTaskAlreadyClaimed) {
		t.Fatalf("expected ErrTaskAlreadyClaimed, got %v", err)
	}
}

func TestConcurrentClaimAllowsOnlyOneWinner(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	t1, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Single winner",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const claimants = 8
	type result struct {
		session string
		err     error
	}
	results := make(chan result, claimants)
	for i := 0; i < claimants; i++ {
		session := fmt.Sprintf("claimer-%d", i)
		go func() {
			_, err := svc.Claim(ctx, wsID, t1.ID, "", session, "")
			results <- result{session: session, err: err}
		}()
	}

	var winners []string
	var losers int
	for i := 0; i < claimants; i++ {
		r := <-results
		switch {
		case r.err == nil:
			winners = append(winners, r.session)
		case errors.Is(r.err, tasks.ErrTaskAlreadyClaimed):
			losers++
		default:
			t.Fatalf("claimant %s got unexpected error: %v", r.session, r.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly one", winners)
	}
	if losers != claimants-1 {
		t.Fatalf("losers = %d, want %d", losers, claimants-1)
	}
	got, err := db.GetTask(ctx, t1.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.AssigneeSessionID != winners[0] {
		t.Fatalf("persisted assignee = %q, winner = %q", got.AssigneeSessionID, winners[0])
	}
	if got.LeaseExpiresAt == nil {
		t.Fatal("claim should stamp a lease")
	}
}

func TestComposeIntoStampsBidirectionalLink(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Epic",
	})
	child, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Sub-task",
		ComposeInto:        parent.ID,
		CreatedBySessionID: "agent-a",
	})

	// Parent should list child in composes
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	composes := tasks.ReadMetaList(parentNow.Meta, "composes")
	if len(composes) != 1 || composes[0] != child.ID {
		t.Fatalf("expected parent.meta.composes=[child], got %v\nmeta=%q", composes, parentNow.Meta)
	}

	// Child should list parent in composed_by
	childNow, _ := svc.Get(ctx, wsID, child.ID)
	composedBy := tasks.ReadMetaList(childNow.Meta, "composed_by")
	if len(composedBy) != 1 || composedBy[0] != parent.ID {
		t.Fatalf("expected child.meta.composed_by=[parent], got %v\nmeta=%q", composedBy, childNow.Meta)
	}
}

func TestComposeIntoIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "A", ComposeInto: parent.ID})

	// Force a duplicate compose by creating another task and pointing it
	// at the SAME child (simulates the same child being added twice — the
	// list should not duplicate ids).
	_, _ = svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "B",
		ComposeInto: parent.ID,
	})
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	// The B task added itself — parent should now have 2 composes, but
	// child's composed_by should still be 1 because we never composed
	// the same child twice.
	if !strings.Contains(parentNow.Meta, child.ID) {
		t.Fatalf("expected child still in parent composes after second compose call")
	}
	childNow, _ := svc.Get(ctx, wsID, child.ID)
	composedBy := tasks.ReadMetaList(childNow.Meta, "composed_by")
	if len(composedBy) != 1 {
		t.Fatalf("expected composed_by len=1 (idempotent), got %d: %v", len(composedBy), composedBy)
	}
}

// TestRollupParentOnAllChildrenClosed verifies the opt-in epic rollup:
// a parent carrying meta.rollup_to flips to that status exactly once
// every composed child is terminal — and not a moment before. This is
// the mechanism that lets a "lander" worker fire on the epic's
// status_to:<target> transition without polling.
func TestRollupParentOnAllChildrenClosed(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Epic", Status: "doing",
		Meta: `{"rollup_to":"review"}`,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	c1, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "child1", ComposeInto: parent.ID})
	c2, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "child2", ComposeInto: parent.ID})

	done := true
	// Close child 1 — one sibling still open, so the parent must NOT roll up.
	if _, err := svc.Update(ctx, wsID, c1.ID, tasks.UpdatePatch{Terminal: &done}); err != nil {
		t.Fatalf("close c1: %v", err)
	}
	if mid, _ := svc.Get(ctx, wsID, parent.ID); mid.Status == "review" {
		t.Fatal("parent rolled up before all children closed")
	}

	// Close child 2 — now all children terminal → parent flips to review.
	if _, err := svc.Update(ctx, wsID, c2.ID, tasks.UpdatePatch{Terminal: &done}); err != nil {
		t.Fatalf("close c2: %v", err)
	}
	pfinal, _ := svc.Get(ctx, wsID, parent.ID)
	if pfinal.Status != "review" {
		t.Fatalf("parent should have rolled up to review, got %q", pfinal.Status)
	}
	if pfinal.ClosedAt != nil {
		t.Fatal("review is non-terminal; rolled-up parent must not be closed")
	}
}

// TestRollupRequiresOptIn confirms a parent WITHOUT meta.rollup_to is
// left untouched when its children close — classic composition keeps its
// pre-existing (no auto-flip) behavior.
func TestRollupRequiresOptIn(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Plain parent", Status: "doing"})
	c1, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "child", ComposeInto: parent.ID})
	done := true
	if _, err := svc.Update(ctx, wsID, c1.ID, tasks.UpdatePatch{Terminal: &done}); err != nil {
		t.Fatalf("close child: %v", err)
	}
	if pnow, _ := svc.Get(ctx, wsID, parent.ID); pnow.Status != "doing" {
		t.Fatalf("non-opted-in parent should stay 'doing', got %q", pnow.Status)
	}
}

// TestCrossWorkspaceAccessIsNotFound — G3 regression. A task in
// workspace A must not be readable / writable / deletable / claimable /
// noteable from workspace B. Cross-workspace ids surface as ErrNotFound
// (not a permission error) so the existence of the row is not leaked.
func TestCrossWorkspaceAccessIsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, db, wsA := newSvc(t)
	wsB := &store.Workspace{Name: "wsB", RootPath: "/tmp/wsB", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	taskA, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsA, Title: "secret", CreatedBySessionID: "a",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Get(ctx, wsB.ID, taskA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get cross-workspace: want ErrNotFound, got %v", err)
	}
	s := "doing"
	if _, err := svc.Update(ctx, wsB.ID, taskA.ID, tasks.UpdatePatch{Status: &s}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update cross-workspace: want ErrNotFound, got %v", err)
	}
	if err := svc.Delete(ctx, wsB.ID, taskA.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete cross-workspace: want ErrNotFound, got %v", err)
	}
	if _, err := svc.Claim(ctx, wsB.ID, taskA.ID, "", "intruder", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Claim cross-workspace: want ErrNotFound, got %v", err)
	}
	if _, err := svc.AppendNote(ctx, wsB.ID, taskA.ID, "hi", "intruder", store.TaskSourceAgent); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AppendNote cross-workspace: want ErrNotFound, got %v", err)
	}
	if _, err := svc.ListNotes(ctx, wsB.ID, taskA.ID, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ListNotes cross-workspace: want ErrNotFound, got %v", err)
	}
	// Sanity: same calls in the correct workspace still work.
	if _, err := svc.Get(ctx, wsA, taskA.ID); err != nil {
		t.Fatalf("Get same-workspace: %v", err)
	}
}

// TestSetWorkContextUpdatesMeta — round-trip a PR url + branch through
// SetWorkContext and verify both that the patch lands in meta AND that
// status_history gets a `work_context_updated` row stamped with the
// author session id. Also verifies the second call updates the
// existing branch line in place rather than appending a duplicate.
func TestSetWorkContextUpdatesMeta(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              "Wire work context",
		CreatedBySessionID: "agent-a",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	patch := tasks.WorkContext{
		PR:     "https://github.com/me/repo/pull/42",
		Branch: "feat/work-context",
	}
	updated, err := svc.SetWorkContext(ctx, wsID, t1.ID, patch, nil, "agent-a")
	if err != nil {
		t.Fatalf("SetWorkContext: %v", err)
	}
	// As of migration 072 meta is JSON-shaped. Walk through
	// ParseWorkContext so the test stays insensitive to key ordering
	// or future shape tweaks (vs. asserting raw substrings).
	updatedWC, err := tasks.ParseWorkContext(updated.Meta)
	if err != nil {
		t.Fatalf("ParseWorkContext(updated.Meta): %v", err)
	}
	if updatedWC.PR != patch.PR {
		t.Fatalf("pr not stored in meta: got %q\n meta=%q", updatedWC.PR, updated.Meta)
	}
	if updatedWC.Branch != patch.Branch {
		t.Fatalf("branch not stored in meta: got %q\n meta=%q", updatedWC.Branch, updated.Meta)
	}
	// Re-read via Get to be sure we're testing what the DB actually
	// persisted (not just an in-memory copy from the Update path).
	roundTrip, err := svc.Get(ctx, wsID, t1.ID)
	if err != nil {
		t.Fatalf("Get post-set: %v", err)
	}
	parsed, err := tasks.ParseWorkContext(roundTrip.Meta)
	if err != nil {
		t.Fatalf("ParseWorkContext: %v", err)
	}
	if parsed.PR != patch.PR || parsed.Branch != patch.Branch {
		t.Fatalf("round-trip mismatch:\nwrote: %+v\n read: %+v", patch, parsed)
	}
	// status_history should now contain a work_context_updated entry.
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(roundTrip.StatusHistoryJSON, &history)
	foundEvt := false
	for _, h := range history {
		if h.Evt == "work_context_updated" {
			foundEvt = true
			if h.BySession != "agent-a" {
				t.Errorf("expected by_session=max, got %q", h.BySession)
			}
		}
	}
	if !foundEvt {
		t.Fatalf("expected work_context_updated history entry, got %+v", history)
	}

	// Second call updates the branch — must not duplicate the key.
	patch2 := tasks.WorkContext{Branch: "feat/work-context-v2"}
	updated2, err := svc.SetWorkContext(ctx, wsID, t1.ID, patch2, nil, "agent-a")
	if err != nil {
		t.Fatalf("SetWorkContext (2nd): %v", err)
	}
	// Walk the typed view — branch should have updated to v2, PR
	// (untouched key) should survive verbatim.
	wc2, err := tasks.ParseWorkContext(updated2.Meta)
	if err != nil {
		t.Fatalf("ParseWorkContext (2nd): %v", err)
	}
	if wc2.Branch != "feat/work-context-v2" {
		t.Fatalf("expected branch=feat/work-context-v2, got %q\n meta=%q", wc2.Branch, updated2.Meta)
	}
	if wc2.PR != patch.PR {
		t.Fatalf("pr line was clobbered by second patch: %+v\n meta=%q", wc2, updated2.Meta)
	}
}

// TestSetWorkContextClearsField — explicit clears slice drops the line
// from meta even when the patch struct can't express that distinction.
func TestSetWorkContextClearsField(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "clear me",
	})
	// Seed branch first.
	if _, err := svc.SetWorkContext(ctx, wsID, t1.ID,
		tasks.WorkContext{Branch: "feat/x"}, nil, "agent-a"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Then clear it via explicit channel.
	cleared, err := svc.SetWorkContext(ctx, wsID, t1.ID,
		tasks.WorkContext{}, []string{"branch"}, "agent-a")
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if strings.Contains(cleared.Meta, "branch:") {
		t.Fatalf("branch line not cleared: %q", cleared.Meta)
	}
}

// TestSetWorkContextRejectsInvalidPR — patch validation rejects before
// touching the DB.
func TestSetWorkContextRejectsInvalidPR(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "reject me",
	})
	_, err := svc.SetWorkContext(ctx, wsID, t1.ID,
		tasks.WorkContext{PR: "not-a-url"}, nil, "agent-a")
	if err == nil {
		t.Fatalf("expected error for invalid PR URL")
	}
}

// TestComposePostHocStampsBidirectionalLink — exported Compose() on
// two existing tasks must touch both meta fields AND append a
// `composed` entry to the parent's status_history. This is the
// post-hoc analogue of TestComposeIntoStampsBidirectionalLink.
func TestComposePostHocStampsBidirectionalLink(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Sub-task"})

	if err := svc.Compose(ctx, wsID, parent.ID, child.ID, "agent-a"); err != nil {
		t.Fatalf("Compose: %v", err)
	}
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	composes := tasks.ReadMetaList(parentNow.Meta, "composes")
	if len(composes) != 1 || composes[0] != child.ID {
		t.Fatalf("expected parent.meta.composes=[child], got %v", composes)
	}
	childNow, _ := svc.Get(ctx, wsID, child.ID)
	composedBy := tasks.ReadMetaList(childNow.Meta, "composed_by")
	if len(composedBy) != 1 || composedBy[0] != parent.ID {
		t.Fatalf("expected child.meta.composed_by=[parent], got %v", composedBy)
	}
	// status_history on the parent must record `composed`.
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(parentNow.StatusHistoryJSON, &history)
	foundComposed := false
	for _, h := range history {
		if h.Evt == "composed" && h.To == child.ID && h.BySession == "agent-a" {
			foundComposed = true
		}
	}
	if !foundComposed {
		t.Fatalf("expected `composed` history entry on parent, got %+v", history)
	}
}

// TestComposePostHocIsIdempotent — calling Compose twice for the same
// (parent, child) pair must not duplicate ids on either side.
func TestComposePostHocIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Sub"})

	for i := 0; i < 3; i++ {
		if err := svc.Compose(ctx, wsID, parent.ID, child.ID, "agent-a"); err != nil {
			t.Fatalf("Compose #%d: %v", i, err)
		}
	}
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	if got := tasks.ReadMetaList(parentNow.Meta, "composes"); len(got) != 1 {
		t.Fatalf("idempotency broken: composes=%v want len=1", got)
	}
	childNow, _ := svc.Get(ctx, wsID, child.ID)
	if got := tasks.ReadMetaList(childNow.Meta, "composed_by"); len(got) != 1 {
		t.Fatalf("idempotency broken: composed_by=%v want len=1", got)
	}
}

// TestComposeRefusesCrossWorkspace — a parent and child in different
// workspaces must not be linkable; surfaces as ErrNotFound to avoid
// leaking cross-workspace existence.
func TestComposeRefusesCrossWorkspace(t *testing.T) {
	ctx := context.Background()
	svc, db, wsA := newSvc(t)
	wsB := &store.Workspace{Name: "wsB", RootPath: "/tmp/wsB", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsA, Title: "P"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsB.ID, Title: "C"})
	// Asking from wsA's perspective: parent is in wsA, child is in wsB.
	// The forward edge resolves parent OK (workspace matches), but the
	// reverse edge on the child should fail. The service implementation
	// gates parent lookup first; cross-workspace surfaces as ErrNotFound
	// when asking from wsB (parent isn't there).
	if err := svc.Compose(ctx, wsB.ID, parent.ID, child.ID, "agent-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Compose cross-workspace (parent not in caller ws): want ErrNotFound, got %v", err)
	}
}

// TestDecomposeRemovesBothMetaFields — Decompose must clean up the
// forward edge AND the reverse edge in one call, and stamp
// `decomposed` on the parent's status_history.
func TestDecomposeRemovesBothMetaFields(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Sub",
		ComposeInto:        parent.ID,
		CreatedBySessionID: "agent-a",
	})
	// Pre-state assertion: links are wired.
	parentBefore, _ := svc.Get(ctx, wsID, parent.ID)
	if got := tasks.ReadMetaList(parentBefore.Meta, "composes"); len(got) != 1 {
		t.Fatalf("precondition: parent.composes=%v want len=1", got)
	}

	if err := svc.Decompose(ctx, wsID, parent.ID, child.ID, "agent-a"); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	if got := tasks.ReadMetaList(parentNow.Meta, "composes"); len(got) != 0 {
		t.Fatalf("parent still has composes after decompose: %v", got)
	}
	childNow, _ := svc.Get(ctx, wsID, child.ID)
	if got := tasks.ReadMetaList(childNow.Meta, "composed_by"); len(got) != 0 {
		t.Fatalf("child still has composed_by after decompose: %v", got)
	}
	// status_history on the parent must record `decomposed`.
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(parentNow.StatusHistoryJSON, &history)
	foundDecomposed := false
	for _, h := range history {
		if h.Evt == "decomposed" && h.From == child.ID {
			foundDecomposed = true
		}
	}
	if !foundDecomposed {
		t.Fatalf("expected `decomposed` history entry on parent, got %+v", history)
	}
}

// TestDecomposeUnlinkedIsNoOp — calling Decompose on a pair that was
// never composed must not error and must not stamp history.
func TestDecomposeUnlinkedIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	a, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "A"})
	b, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "B"})

	if err := svc.Decompose(ctx, wsID, a.ID, b.ID, "agent-a"); err != nil {
		t.Fatalf("Decompose unlinked: want nil, got %v", err)
	}
	aNow, _ := svc.Get(ctx, wsID, a.ID)
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(aNow.StatusHistoryJSON, &history)
	for _, h := range history {
		if h.Evt == "decomposed" {
			t.Fatalf("no-op decompose still stamped history: %+v", h)
		}
	}
}

// TestDecomposeRefusesCrossWorkspace — same posture as Compose.
func TestDecomposeRefusesCrossWorkspace(t *testing.T) {
	ctx := context.Background()
	svc, db, wsA := newSvc(t)
	wsB := &store.Workspace{Name: "wsB", RootPath: "/tmp/wsB", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsA, Title: "P"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsB.ID, Title: "C"})
	if err := svc.Decompose(ctx, wsB.ID, parent.ID, child.ID, "agent-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Decompose cross-workspace: want ErrNotFound, got %v", err)
	}
}

// TestBulkComposeReportsPerIDFailures — bulk form returns {ok, failed}
// when some child ids are valid and others aren't. Mirrors task__update's
// bulk pattern.
func TestBulkComposeReportsPerIDFailures(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	c1, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "C1"})
	c2, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "C2"})

	ok, failed := svc.BulkCompose(ctx, wsID, parent.ID, []string{c1.ID, "nonexistent-id", c2.ID}, "agent-a")
	if len(ok) != 2 {
		t.Fatalf("expected 2 successes, got %d: %v", len(ok), ok)
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 failure, got %d: %v", len(failed), failed)
	}
	if failed[0].ID != "nonexistent-id" {
		t.Fatalf("expected failed id to be the missing one, got %q", failed[0].ID)
	}
	// Verify parent now has both real children.
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	composes := tasks.ReadMetaList(parentNow.Meta, "composes")
	if len(composes) != 2 {
		t.Fatalf("expected 2 composes after bulk, got %d: %v", len(composes), composes)
	}
}

// TestRemoveMetaListLineDropsEmptyKey — verifies the meta cleanup
// behaviour exposed via Decompose: when removing the only value on a
// list line, the whole `key:` stub is dropped so meta doesn't fill up
// with dangling frontmatter.
func TestRemoveMetaListLineDropsEmptyKey(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	parent, _ := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "Epic"})
	child, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Only", ComposeInto: parent.ID,
	})
	if err := svc.Decompose(ctx, wsID, parent.ID, child.ID, "agent-a"); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	parentNow, _ := svc.Get(ctx, wsID, parent.ID)
	if strings.Contains(parentNow.Meta, "composes:") {
		t.Fatalf("expected dangling `composes:` line to be dropped, got meta=%q", parentNow.Meta)
	}
	childNow, _ := svc.Get(ctx, wsID, child.ID)
	if strings.Contains(childNow.Meta, "composed_by:") {
		t.Fatalf("expected dangling `composed_by:` line to be dropped, got meta=%q", childNow.Meta)
	}
}

func TestKnownStatusesIncludesVocabAndCommonFallback(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	if err := db.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "shipped", IsTerminal: true,
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab: %v", err)
	}
	statuses, err := svc.KnownStatuses(ctx, wsID)
	if err != nil {
		t.Fatalf("KnownStatuses: %v", err)
	}
	// "shipped" from vocab first, then the common fallback (open/doing/etc).
	if statuses[0] != "shipped" {
		t.Fatalf("expected vocab entry first, got %v", statuses)
	}
	hasOpen := false
	for _, s := range statuses {
		if s == "open" {
			hasOpen = true
		}
	}
	if !hasOpen {
		t.Fatalf("expected common 'open' in fallback, got %v", statuses)
	}
}

// ----------------------------------------------------------------------
// discovery envelope helpers — pin the dedupe + filter rules so a
// future refactor that re-inflates the envelope back to its old size
// fails loudly. Each helper is a pure function (no DB) so the tests
// stay fast + readable.
// ----------------------------------------------------------------------

func TestFilterKnownAssigneesDropsEmptyAndDedupes(t *testing.T) {
	t0 := mustParse(t, "2026-05-25T19:00:00Z")
	in := []store.MeshAgent{
		// Stale-but-recent dup of "sess-a": newer LastSeenAt should win.
		{SessionID: "sess-a", Name: "alpha", LastSeenAt: t0.Add(-2 * time.Minute), Origin: "peer:abc"},
		{SessionID: "sess-a", Name: "alpha-refreshed", LastSeenAt: t0, Origin: "peer:abc"},
		// Both name + session blank → dropped.
		{SessionID: "", Name: "", LastSeenAt: t0},
		// Whitespace-only name + session → also dropped.
		{SessionID: " ", Name: "  ", LastSeenAt: t0},
		{SessionID: "sess-b", Name: "beta", LastSeenAt: t0.Add(-1 * time.Minute)},
		// Anonymous-but-named row → kept under name-key; two with same
		// name dedupe by most-recent.
		{SessionID: "", Name: "carl", LastSeenAt: t0.Add(-30 * time.Second)},
		{SessionID: "", Name: "carl", LastSeenAt: t0.Add(-3 * time.Second)},
	}
	out := tasks.FilterKnownAssignees(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 deduped entries (sess-a, sess-b, carl), got %d: %+v", len(out), out)
	}
	// Most-recent first.
	if out[0].SessionID != "sess-a" || out[0].Name != "alpha-refreshed" {
		t.Fatalf("expected sess-a (refreshed) first, got %+v", out[0])
	}
	if out[1].Name != "carl" || out[1].SessionID != "" {
		t.Fatalf("expected anonymous carl second, got %+v", out[1])
	}
	if out[2].SessionID != "sess-b" {
		t.Fatalf("expected sess-b third, got %+v", out[2])
	}
	if out[0].PeerID != "abc" {
		t.Fatalf("expected PeerID populated from origin=peer:abc, got %q", out[0].PeerID)
	}
}

func TestFilterKnownAssigneesCapsAtFive(t *testing.T) {
	t0 := mustParse(t, "2026-05-25T19:00:00Z")
	in := make([]store.MeshAgent, 0, 10)
	for i := 0; i < 10; i++ {
		in = append(in, store.MeshAgent{
			SessionID:  "sess-" + string(rune('a'+i)),
			Name:       "agent-" + string(rune('a'+i)),
			LastSeenAt: t0.Add(time.Duration(-i) * time.Minute),
		})
	}
	out := tasks.FilterKnownAssignees(in)
	if len(out) != tasks.MaxKnownAssignees {
		t.Fatalf("expected cap=%d entries, got %d", tasks.MaxKnownAssignees, len(out))
	}
	// Cap should keep the 5 most-recent → sess-a..sess-e.
	wantPrefix := "sess-a"
	if out[0].SessionID != wantPrefix {
		t.Fatalf("expected most-recent (%s) first, got %q", wantPrefix, out[0].SessionID)
	}
}

func TestFilterKnownAssigneesEmptyInput(t *testing.T) {
	if got := tasks.FilterKnownAssignees(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %+v", got)
	}
	allEmpty := []store.MeshAgent{{}, {}, {}}
	if got := tasks.FilterKnownAssignees(allEmpty); len(got) != 0 {
		t.Fatalf("expected zero entries when every row is blank, got %+v", got)
	}
}

func TestFilterKnownTagsUnionRowsAndTopN(t *testing.T) {
	rows := []store.Task{
		{TagsJSON: json.RawMessage(`["bug","ui"]`)},
		{TagsJSON: json.RawMessage(`["ui","perf"]`)},
		{TagsJSON: json.RawMessage(`["docs"]`)},
		// malformed → tolerated, no panic.
		{TagsJSON: json.RawMessage(`not-json`)},
	}
	// Pretend "infra" is workspace-popular but isn't in the result rows.
	topN := []string{"infra", "ui", ""}
	out := tasks.FilterKnownTags(rows, topN)
	want := []string{"bug", "docs", "infra", "perf", "ui"}
	if !equalStrings(out, want) {
		t.Fatalf("expected %v, got %v", want, out)
	}
}

func TestFilterKnownStatusesUnionRowsAndVocab(t *testing.T) {
	rows := []store.Task{
		{Status: "triaging"}, {Status: "doing"}, {Status: ""},
	}
	vocab := []string{"open", "doing", "blocked", "review", "done", "cancelled", "triaging"}
	out := tasks.FilterKnownStatuses(rows, vocab)
	// Default vocab first in canonical order, then any extras alphabetised.
	want := []string{"open", "doing", "blocked", "review", "done", "cancelled", "triaging"}
	if !equalStrings(out, want) {
		t.Fatalf("expected %v, got %v", want, out)
	}
}

func TestFilterKnownStatusesOmitsAbsentDefaults(t *testing.T) {
	// No rows, no vocab → empty (helper returns "things you might use"
	// — if nothing's in use it returns nothing).
	out := tasks.FilterKnownStatuses(nil, nil)
	if len(out) != 0 {
		t.Fatalf("expected empty result for empty input, got %v", out)
	}
}

func TestTopWorkspaceTagsRanksByFrequency(t *testing.T) {
	rows := []store.Task{
		{TagsJSON: json.RawMessage(`["ui","perf","ui"]`)},
		{TagsJSON: json.RawMessage(`["ui","docs"]`)},
		{TagsJSON: json.RawMessage(`["perf"]`)},
	}
	out := tasks.TopWorkspaceTags(rows, 5)
	// ui=3, perf=2, docs=1.
	want := []string{"ui", "perf", "docs"}
	if !equalStrings(out, want) {
		t.Fatalf("expected %v, got %v", want, out)
	}
	// Cap at 2 → keep the two most frequent.
	out = tasks.TopWorkspaceTags(rows, 2)
	if !equalStrings(out, []string{"ui", "perf"}) {
		t.Fatalf("expected top-2 [ui perf], got %v", out)
	}
}

// TestSweepExpiredLeasesDemotesWorkingStatusRow pins the behaviour
// added in 5f3154b: when the passive sweep finds an expired lease on
// a working-status task, the status is demoted back to "open" AND
// status_history records both a status_changed and a lease_expired
// entry. Backdates the lease by calling HeartbeatTask with a negative
// TTL — the cleanest public-API way to make `lease_expires_at` be in
// the past without time-injection.
func TestSweepExpiredLeasesDemotesWorkingStatusRow(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Lease will expire", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "owner-session", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Backdate the lease so the sweep sees it as expired.
	if _, err := db.HeartbeatTask(ctx, t1.ID, "owner-session", -1*time.Hour); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	swept, err := svc.SweepExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("expected 1 swept row, got %d", swept)
	}

	got, err := db.GetTask(ctx, t1.ID)
	if err != nil {
		t.Fatalf("re-read task: %v", err)
	}
	if got.Status != "open" {
		t.Errorf("expected status demoted to 'open', got %q", got.Status)
	}
	if got.AssigneeSessionID != "" {
		t.Errorf("expected assignee cleared, got %q", got.AssigneeSessionID)
	}

	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(got.StatusHistoryJSON, &history)
	var sawDemotion, sawExpiry bool
	for _, h := range history {
		if h.Evt == "status_changed" && h.From == "doing" && h.To == "open" {
			sawDemotion = true
		}
		if h.Evt == "lease_expired" {
			sawExpiry = true
		}
	}
	if !sawDemotion {
		t.Errorf("expected status_changed doing→open entry in history, got %+v", history)
	}
	if !sawExpiry {
		t.Errorf("expected lease_expired entry in history, got %+v", history)
	}
}

// TestSweepExpiredLeasesLeavesNonWorkingStatusUnchanged pins
// acceptance criterion (b): when a row in a NON-working status (e.g.
// "blocked") has an expired lease, the sweep clears the lease but must
// NOT demote the status. Only working-status rows get pushed back to
// "open"; a blocked row that loses its owner stays blocked so the
// block reason isn't silently erased.
func TestSweepExpiredLeasesLeavesNonWorkingStatusUnchanged(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Blocked but still leased", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "owner-session", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Move to a non-working status while keeping the lease + assignee.
	blocked := "blocked"
	if _, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status: &blocked, UpdatedBySessionID: "owner-session",
	}); err != nil {
		t.Fatalf("update to blocked: %v", err)
	}
	// Precondition: the blocked row must still own its lease, otherwise
	// the sweep wouldn't see it and the assertion below would pass
	// vacuously.
	mid, _ := db.GetTask(ctx, t1.ID)
	if mid.AssigneeSessionID != "owner-session" {
		t.Fatalf("setup: blocked row should retain its assignee, got %q", mid.AssigneeSessionID)
	}
	// Backdate the lease so the sweep treats it as expired.
	if _, err := db.HeartbeatTask(ctx, t1.ID, "owner-session", -1*time.Hour); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	swept, err := svc.SweepExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if swept != 1 {
		t.Fatalf("expected 1 swept row, got %d", swept)
	}

	got, err := db.GetTask(ctx, t1.ID)
	if err != nil {
		t.Fatalf("re-read task: %v", err)
	}
	if got.Status != "blocked" {
		t.Errorf("non-working status must survive the sweep, got %q", got.Status)
	}
	if got.AssigneeSessionID != "" {
		t.Errorf("an expired lease must still clear the assignee, got %q", got.AssigneeSessionID)
	}

	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(got.StatusHistoryJSON, &history)
	var sawExpiry, sawDemotion bool
	for _, h := range history {
		if h.Evt == "lease_expired" {
			sawExpiry = true
		}
		if h.Evt == "status_changed" && h.To == "open" {
			sawDemotion = true
		}
	}
	if !sawExpiry {
		t.Errorf("expected lease_expired entry in history, got %+v", history)
	}
	if sawDemotion {
		t.Errorf("non-working row must NOT record a demotion to open, got %+v", history)
	}
}

// TestReleaseSessionTasksScopesToSession pins 5f3154b's disconnect
// hook: ReleaseSessionTasks must clear leases ONLY for the named
// session, demote working-status rows to "open", and never disturb
// tasks owned by other sessions.
func TestReleaseSessionTasksScopesToSession(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	tMine, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Owned by me", CreatedBySessionID: "agent-a",
	})
	tTheirs, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Owned by peer", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, tMine.ID, "", "me-session", ""); err != nil {
		t.Fatalf("claim mine: %v", err)
	}
	if _, err := svc.Claim(ctx, wsID, tTheirs.ID, "", "peer-session", ""); err != nil {
		t.Fatalf("claim theirs: %v", err)
	}

	n, err := svc.ReleaseSessionTasks(ctx, "me-session")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 task released for me-session, got %d", n)
	}

	mineAfter, _ := db.GetTask(ctx, tMine.ID)
	if mineAfter.Status != "open" {
		t.Errorf("my task should be demoted to open, got %q", mineAfter.Status)
	}
	if mineAfter.AssigneeSessionID != "" {
		t.Errorf("my assignee should be cleared, got %q", mineAfter.AssigneeSessionID)
	}

	theirsAfter, _ := db.GetTask(ctx, tTheirs.ID)
	if theirsAfter.Status != "doing" {
		t.Errorf("peer's task must remain doing, got %q", theirsAfter.Status)
	}
	if theirsAfter.AssigneeSessionID != "peer-session" {
		t.Errorf("peer's task must keep its assignee, got %q", theirsAfter.AssigneeSessionID)
	}

	// Disconnect history note must reference the disconnect, not lease
	// expiry — this is the signal the dashboard uses to render the
	// difference. (Demoted-from-working entry comes first; the
	// trailing lease_expired entry carries the "released on disconnect"
	// note.)
	var history []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(mineAfter.StatusHistoryJSON, &history)
	var sawDisconnectNote bool
	for _, h := range history {
		if h.Evt == "status_changed" && strings.Contains(h.Note, "disconnect") {
			sawDisconnectNote = true
		}
	}
	if !sawDisconnectNote {
		t.Errorf("expected disconnect-flavored status_changed note, got %+v", history)
	}
}

// TestReleaseSessionTasksEmptySessionIDIsNoOp guards the defensive
// early-return: an empty sessionID must never accidentally clear
// every lease in the workspace.
func TestReleaseSessionTasksEmptySessionIDIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Should survive", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "owner", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	n, err := svc.ReleaseSessionTasks(ctx, "")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 released for empty sessionID, got %d", n)
	}
	got, _ := db.GetTask(ctx, t1.ID)
	if got.Status != "doing" {
		t.Errorf("task must remain doing, got %q", got.Status)
	}
	if got.AssigneeSessionID != "owner" {
		t.Errorf("assignee must remain 'owner', got %q", got.AssigneeSessionID)
	}
}

// TestReleaseSessionTasksWithReasonStampsNote is the per-row table check
// that the daemon-drain reason ("daemon restarting") lands on every demoted
// task's status_history. The note is what makes the difference between
// "a peer agent died mid-call" and "I (the daemon) restarted the box"
// visible to whoever picks the task up next. Each case is verified
// independently so a single typo can't quietly pass.
//
// The empty-reason case exercises back-compat with the default
// ReleaseSessionTasks call sites (gateway per-session disconnect defers,
// passive lease sweep) — they must keep getting the legacy notes.
func TestReleaseSessionTasksWithReasonStampsNote(t *testing.T) {
	cases := []struct {
		name       string
		reason     string
		wantDemote string
		wantLease  string
	}{
		{
			name:       "empty reason falls back to disconnect defaults",
			reason:     "",
			wantDemote: "agent disconnected, demoted from working status",
			wantLease:  "released on disconnect",
		},
		{
			name:       "daemon restarting stamps both history entries",
			reason:     "daemon restarting",
			wantDemote: "daemon restarting: demoted from working status",
			wantLease:  "daemon restarting",
		},
		{
			name:       "operator-supplied reason flows through verbatim",
			reason:     "scheduled maintenance",
			wantDemote: "scheduled maintenance: demoted from working status",
			wantLease:  "scheduled maintenance",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			svc, db, wsID := newSvc(t)
			task1, _ := svc.Create(ctx, tasks.CreateOptions{
				WorkspaceID:        wsID,
				Title:              "Task to be released",
				CreatedBySessionID: "agent-a",
			})
			if _, err := svc.Claim(ctx, wsID, task1.ID, "", "daemon-self", ""); err != nil {
				t.Fatalf("claim: %v", err)
			}

			n, err := svc.ReleaseSessionTasksWithReason(ctx, "daemon-self", c.reason)
			if err != nil {
				t.Fatalf("release: %v", err)
			}
			if n != 1 {
				t.Fatalf("expected 1 task released, got %d", n)
			}

			after, _ := db.GetTask(ctx, task1.ID)
			if after.Status != "open" {
				t.Fatalf("status=%q want open", after.Status)
			}
			if after.AssigneeSessionID != "" {
				t.Fatalf("assignee=%q want cleared", after.AssigneeSessionID)
			}

			var history []store.TaskStatusHistoryEntry
			_ = json.Unmarshal(after.StatusHistoryJSON, &history)

			var sawDemote, sawLease bool
			for _, h := range history {
				if h.Evt == "status_changed" && h.Note == c.wantDemote {
					sawDemote = true
				}
				if h.Evt == "lease_expired" && h.Note == c.wantLease {
					sawLease = true
				}
			}
			if !sawDemote {
				t.Errorf("missing demote note %q in history %+v", c.wantDemote, history)
			}
			if !sawLease {
				t.Errorf("missing lease note %q in history %+v", c.wantLease, history)
			}
		})
	}
}

// TestReleaseSessionTasksWithReasonEmptySessionIDIsNoOp mirrors the
// disconnect variant: an empty sessionID must NOT match every row in the
// workspace, regardless of reason.
func TestReleaseSessionTasksWithReasonEmptySessionIDIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Survives", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, t1.ID, "", "owner", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	n, err := svc.ReleaseSessionTasksWithReason(ctx, "", "daemon restarting")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 released for empty sessionID, got %d", n)
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return got
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCloseTerminalVocabGuardReservedStatus(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	t1, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Open task", Status: "open",
		CreatedBySessionID: "s1",
	})
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}

	term := true
	closed, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Terminal:           &term,
		UpdatedBySessionID: "s1",
	})
	if err != nil {
		t.Fatalf("update terminal:true: %v", err)
	}
	if closed.ClosedAt == nil {
		t.Fatal("expected ClosedAt to be set after terminal close")
	}

	isTerm, _ := db.IsTerminalStatus(ctx, wsID, "open")
	if isTerm {
		t.Fatal("reserved status 'open' must NOT be marked terminal in vocab")
	}

	t2, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Second open task", Status: "open",
		CreatedBySessionID: "s1",
	})
	if err != nil {
		t.Fatalf("create t2: %v", err)
	}

	nonTerm := false
	openTasks, err := svc.List(ctx, store.TaskFilter{WorkspaceID: wsID, OnlyTerminal: &nonTerm})
	if err != nil {
		t.Fatalf("list non-terminal: %v", err)
	}
	ids := make(map[string]bool)
	for _, t := range openTasks {
		ids[t.ID] = true
	}
	if ids[t1.ID] {
		t.Error("closed task t1 should NOT appear in non-terminal list (ClosedAt is set)")
	}
	if !ids[t2.ID] {
		t.Error("task t2 with status 'open' must still appear in non-terminal list — vocab was not poisoned")
	}
}

func TestCloseTerminalVocabCustomStatusGetsMarked(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)

	t1, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "Shipped task", Status: "shipped",
		CreatedBySessionID: "s1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	term := true
	closed, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Terminal:           &term,
		UpdatedBySessionID: "s1",
	})
	if err != nil {
		t.Fatalf("update terminal:true: %v", err)
	}
	if closed.ClosedAt == nil {
		t.Fatal("expected ClosedAt to be set")
	}

	isTerm, _ := db.IsTerminalStatus(ctx, wsID, "shipped")
	if !isTerm {
		t.Fatal("custom status 'shipped' SHOULD be marked terminal on close")
	}

	vocab, _ := db.ListTaskStatusVocab(ctx, wsID)
	var shippedVocab *store.TaskStatusVocab
	for i := range vocab {
		if vocab[i].StatusText == "shipped" {
			shippedVocab = &vocab[i]
			break
		}
	}
	if shippedVocab == nil {
		t.Fatal("expected vocab entry for 'shipped'")
	}
	if shippedVocab.Kind != tasks.KindDone {
		t.Errorf("expected Kind=%q for custom terminal status, got %q", tasks.KindDone, shippedVocab.Kind)
	}
}

// --- Human assignee tests (migration 105) ---

func TestCreateWithHumanAssignee(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	got, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              "Human task",
		CreatedBySessionID: "agent-a",
		Assignee:           &tasks.Assignee{UserID: "user-123"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.AssigneeUserID != "user-123" {
		t.Fatalf("expected assignee_user_id=user-123, got %q", got.AssigneeUserID)
	}
	if got.AssigneeOriginKind != store.TaskAssigneeHuman {
		t.Fatalf("expected origin_kind=human, got %q", got.AssigneeOriginKind)
	}
	if got.AssigneeSessionID != "" {
		t.Fatalf("expected empty session_id, got %q", got.AssigneeSessionID)
	}
}

func TestUpdateWithHumanAssignee(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Update test",
	})
	got, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Assignee: &tasks.Assignee{UserID: "user-456"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.AssigneeUserID != "user-456" {
		t.Fatalf("expected assignee_user_id=user-456, got %q", got.AssigneeUserID)
	}
	if got.AssigneeOriginKind != store.TaskAssigneeHuman {
		t.Fatalf("expected origin_kind=human, got %q", got.AssigneeOriginKind)
	}
}

func TestClearHumanAssignee(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Clear test",
		Assignee:    &tasks.Assignee{UserID: "user-789"},
	})
	got, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Clear: []string{"assignee"},
	})
	if err != nil {
		t.Fatalf("Update clear: %v", err)
	}
	if got.AssigneeUserID != "" {
		t.Fatalf("expected empty assignee_user_id after clear, got %q", got.AssigneeUserID)
	}
	if got.AssigneeOriginKind != store.TaskAssigneeLocal {
		t.Fatalf("expected origin_kind=local after clear, got %q", got.AssigneeOriginKind)
	}
}

func TestClaimRefusesHumanAssignedTask(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Human owned",
		Assignee:    &tasks.Assignee{UserID: "user-human"},
	})
	_, err := svc.Claim(ctx, wsID, t1.ID, "", "max-session", "")
	if !errors.Is(err, tasks.ErrTaskAlreadyClaimed) {
		t.Fatalf("expected ErrTaskAlreadyClaimed for human-assigned task, got %v", err)
	}
}

func TestAutoClaimSkipsHumanAssignedTask(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Human task",
		Assignee:    &tasks.Assignee{UserID: "user-999"},
	})
	got, err := svc.Update(ctx, wsID, t1.ID, tasks.UpdatePatch{
		Status: strPtr("doing"),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.AssigneeUserID != "user-999" {
		t.Fatalf("expected human assignee preserved, got user_id=%q", got.AssigneeUserID)
	}
	if got.AssigneeSessionID != "" {
		t.Fatalf("expected no auto-claim, got session_id=%q", got.AssigneeSessionID)
	}
}

func TestAssigneeDisplayHuman(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Display test",
		Assignee:    &tasks.Assignee{UserID: "user-42"},
	})
	var history []store.TaskStatusHistoryEntry
	if err := json.Unmarshal(t1.StatusHistoryJSON, &history); err != nil {
		t.Fatalf("history json: %v", err)
	}
	found := false
	for _, h := range history {
		if h.Evt == "assigned" && h.To == "user:user-42" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected assigned event with user:user-42, got history=%+v", history)
	}
}

func TestListTasksFilterByAssigneeUserID(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	_, _ = svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Human task",
		Assignee:    &tasks.Assignee{UserID: "user-A"},
	})
	_, _ = svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Other task",
	})
	rows, err := svc.List(ctx, store.TaskFilter{
		WorkspaceID:    wsID,
		AssigneeUserID: "user-A",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 task, got %d", len(rows))
	}
	if rows[0].AssigneeUserID != "user-A" {
		t.Fatalf("expected assignee_user_id=user-A, got %q", rows[0].AssigneeUserID)
	}
}

func strPtr(s string) *string { return &s }

// TestClaimTask_StoreLevelCAS is a store-level test that verifies
// ClaimTask's CAS guard directly, bypassing the service layer.
func TestClaimTask_StoreLevelCAS(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	w := &store.Workspace{Name: "ws1", RootPath: "/tmp/ws1"}
	if err := d.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	task := &store.Task{
		WorkspaceID: w.ID,
		Title:       "Unclaimed",
		Status:      "open",
	}
	if err := d.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Prepare a claimed version of the task.
	claimed := &store.Task{
		ID:                  task.ID,
		WorkspaceID:         task.WorkspaceID,
		Title:               task.Title,
		Status:              "doing",
		AssigneeSessionID:   "session-A",
		AssigneeOriginKind:  store.TaskAssigneeLocal,
		AssignedBySessionID: "session-A",
		HlcAt:               clock.Now(),
	}
	history := []store.TaskStatusHistoryEntry{
		{At: time.Now().UTC(), Evt: "created", To: "open"},
		{At: time.Now().UTC(), Evt: "status_changed", From: "open", To: "doing"},
	}
	claimed.StatusHistoryJSON, _ = json.Marshal(history)

	// First claim should succeed.
	if err := d.ClaimTask(ctx, claimed, "session-A"); err != nil {
		t.Fatalf("first ClaimTask: %v", err)
	}

	// Second claim (same row, different session) should fail with CAS.
	competing := &store.Task{
		ID:                  task.ID,
		WorkspaceID:         task.WorkspaceID,
		Title:               task.Title,
		Status:              "doing",
		AssigneeSessionID:   "session-B",
		AssigneeOriginKind:  store.TaskAssigneeLocal,
		AssignedBySessionID: "session-B",
		HlcAt:               clock.Now(),
	}
	competing.StatusHistoryJSON = claimed.StatusHistoryJSON

	err = d.ClaimTask(ctx, competing, "session-B")
	if !errors.Is(err, store.ErrTaskAlreadyClaimed) {
		t.Fatalf("expected ErrTaskAlreadyClaimed for competing claim, got %v", err)
	}

	// Verify the row still belongs to session-A.
	row, err := d.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.AssigneeSessionID != "session-A" {
		t.Fatalf("expected assignee=session-A after failed CAS, got %q", row.AssigneeSessionID)
	}
}

// TestClaim_IdempotentReclaim verifies that the same session can
// re-claim a task it already owns (the OR assignee_session_id = ?
// arm of the CAS guard).
func TestClaim_IdempotentReclaim(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	task, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "reclaim me",
	})
	_, err := svc.Claim(ctx, wsID, task.ID, "", "owner", "")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// Same session re-claims — should succeed (idempotent).
	reclaimed, err := svc.Claim(ctx, wsID, task.ID, "", "owner", "re-claiming")
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if reclaimed.AssigneeSessionID != "owner" {
		t.Fatalf("expected assignee=owner, got %q", reclaimed.AssigneeSessionID)
	}
}
