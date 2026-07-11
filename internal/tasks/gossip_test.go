package tasks_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newGossipTestSvc returns a Service backed by a real in-memory sqlite
// store + a seeded workspace; cleanup wires via t.Cleanup.
func newGossipTestSvc(t *testing.T) (*tasks.Service, *sqlite.DB, string) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w := &store.Workspace{Name: "gossip-ws", RootPath: "/tmp/gossip", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	svc := tasks.New(d)
	svc.SetBus(tasks.NewBus())
	return svc, d, w.ID
}

// seedTask creates a local task and returns its post-create row.
func seedTask(t *testing.T, svc *tasks.Service, workspaceID, title string) *store.Task {
	t.Helper()
	row, err := svc.Create(context.Background(), tasks.CreateOptions{
		WorkspaceID:        workspaceID,
		Title:              title,
		Status:             "open",
		CreatedBySessionID: "seed-session",
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return row
}

// buildEvent constructs a TaskSyncEvent with the given patch + HLC +
// peer. JSON-encodes patch into FieldPatchesJSON.
func buildEvent(t *testing.T, taskID, workspaceID, hlc, byPeer string, patch tasks.RemoteTaskPatch) p2p.TaskSyncEvent {
	t.Helper()
	raw, err := json.Marshal(&patch)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	return p2p.TaskSyncEvent{
		Type:             "task_event",
		TaskID:           taskID,
		WorkspaceID:      workspaceID,
		HLC:              hlc,
		BySession:        "remote-session",
		ByPeer:           byPeer,
		FieldPatchesJSON: raw,
	}
}

// TestApplyRemoteEvent_StaleNoop verifies a stale event (HLC lower
// than the local row's HLC) is dropped without mutating anything.
// Without this guarantee, a re-replay of an older mesh message would
// silently regress local state.
func TestApplyRemoteEvent_StaleNoop(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "stay put")
	// Synthesize an event whose HLC is strictly less than the row's.
	stale := clock.Format(0, 0) // smallest possible HLC
	evt := buildEvent(t, row.ID, wsID, stale, "peer-A", tasks.RemoteTaskPatch{Title: "remote stomp"})

	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply stale: %v", err)
	}
	after, err := svc.Get(ctx, wsID, row.ID)
	if err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	if after.Title != "stay put" {
		t.Fatalf("stale event mutated title: got %q, want %q", after.Title, "stay put")
	}
	if after.HlcAt != row.HlcAt {
		t.Fatalf("stale event mutated HLC: got %q, want %q", after.HlcAt, row.HlcAt)
	}
}

// TestApplyRemoteEvent_NewerWins verifies an event with a higher HLC
// replaces the matching local fields. The patch only sets Title +
// Priority — other fields must be preserved (no nil-stomping).
func TestApplyRemoteEvent_NewerWins(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "before")
	newerHLC := clock.Now()
	if newerHLC <= row.HlcAt {
		t.Fatalf("test clock didn't advance past row HLC: %q vs %q", newerHLC, row.HlcAt)
	}
	evt := buildEvent(t, row.ID, wsID, newerHLC, "peer-A", tasks.RemoteTaskPatch{
		Title:    "after",
		Priority: "high",
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply newer: %v", err)
	}
	after, err := svc.Get(ctx, wsID, row.ID)
	if err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	if after.Title != "after" {
		t.Fatalf("newer title not applied: got %q", after.Title)
	}
	if after.Priority != "high" {
		t.Fatalf("newer priority not applied: got %q", after.Priority)
	}
	if after.HlcAt != newerHLC {
		t.Fatalf("HLC not advanced: got %q, want %q", after.HlcAt, newerHLC)
	}
}

// TestApplyRemoteEvent_SameHLCSmallerPeerWins exercises the tiebreak:
// when two peers stamp the same HLC for the same task, the
// lexically-smaller peer id wins (deterministic convergence). We seed
// the local row with the "larger" peer id and then apply an event
// claiming the smaller peer — the patch must apply.
func TestApplyRemoteEvent_SameHLCSmallerPeerWins(t *testing.T) {
	ctx := context.Background()
	svc, d, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "tied")
	// Stamp the AssigneePeerID + clear HlcAt so the store re-stamps
	// and we can capture the new value as the tied baseline.
	row.AssigneePeerID = "peer-zz"
	row.AssigneeOriginKind = store.TaskAssigneePeer
	row.HlcAt = ""
	if err := d.UpdateTask(ctx, row); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	// Read back to grab the freshly-stamped HLC.
	reloaded, err := d.GetTask(ctx, row.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	tiedHLC := reloaded.HlcAt
	evt := buildEvent(t, row.ID, wsID, tiedHLC, "peer-aa", tasks.RemoteTaskPatch{
		Title: "smaller peer wins",
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-aa", evt); err != nil {
		t.Fatalf("apply tied: %v", err)
	}
	after, err := svc.Get(ctx, wsID, row.ID)
	if err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	if after.Title != "smaller peer wins" {
		t.Fatalf("smaller-peer tiebreak failed: title got %q, want %q",
			after.Title, "smaller peer wins")
	}
}

// TestApplyRemoteEvent_SameHLCLargerPeerLoses is the mirror — a
// lexically-larger peer id MUST NOT overwrite an existing row with
// the same HLC.
func TestApplyRemoteEvent_SameHLCLargerPeerLoses(t *testing.T) {
	ctx := context.Background()
	svc, d, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "keep")
	row.AssigneePeerID = "peer-aa" // smaller — should win
	row.AssigneeOriginKind = store.TaskAssigneePeer
	row.HlcAt = ""
	if err := d.UpdateTask(ctx, row); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	reloaded, _ := d.GetTask(ctx, row.ID)
	tiedHLC := reloaded.HlcAt
	evt := buildEvent(t, row.ID, wsID, tiedHLC, "peer-zz", tasks.RemoteTaskPatch{
		Title: "larger peer should not win",
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-zz", evt); err != nil {
		t.Fatalf("apply: %v", err)
	}
	after, _ := svc.Get(ctx, wsID, row.ID)
	if after.Title == "larger peer should not win" {
		t.Fatalf("larger peer wrongly won tiebreak; title got %q", after.Title)
	}
}

// TestApplyRemoteEvent_DedupeBySameTuple confirms idempotence: the
// same (task_id, hlc, by_peer) replayed twice must produce a single
// observable state change. Critical for libp2p retry / replay
// resilience.
func TestApplyRemoteEvent_DedupeBySameTuple(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "before")
	newerHLC := clock.Now()
	evt := buildEvent(t, row.ID, wsID, newerHLC, "peer-A", tasks.RemoteTaskPatch{Title: "after"})

	// First apply — must mutate.
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	after1, _ := svc.Get(ctx, wsID, row.ID)
	if after1.Title != "after" {
		t.Fatalf("first apply didn't mutate")
	}
	// Second apply — must be a no-op (HLC + peer identical).
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	after2, _ := svc.Get(ctx, wsID, row.ID)
	if after2.HlcAt != after1.HlcAt {
		t.Fatalf("redundant apply advanced HLC: %q -> %q", after1.HlcAt, after2.HlcAt)
	}
	// History grew by exactly one gossip entry on the first apply; the
	// second must NOT append.
	var hist1, hist2 []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(after1.StatusHistoryJSON, &hist1)
	_ = json.Unmarshal(after2.StatusHistoryJSON, &hist2)
	if len(hist1) != len(hist2) {
		t.Fatalf("dedupe-aware apply still appended history: %d -> %d",
			len(hist1), len(hist2))
	}
}

// TestApplyRemoteEvent_MaterializeNewTask exercises the "new row from
// peer" path: a remote event references a task we've never seen. The
// receiver must materialize it with the remote HLC and a
// peer-import provenance tag so the dashboard can show "received
// via gossip" instead of "created here".
func TestApplyRemoteEvent_MaterializeNewTask(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newGossipTestSvc(t)

	remoteHLC := clock.Now()
	const remoteID = "01TASKFROMREMOTE000000000000"
	evt := buildEvent(t, remoteID, wsID, remoteHLC, "peer-A", tasks.RemoteTaskPatch{
		Title:    "born remote",
		Status:   "open",
		Priority: "normal",
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	row, err := svc.Get(ctx, wsID, remoteID)
	if err != nil {
		t.Fatalf("get materialized: %v", err)
	}
	if row.Title != "born remote" {
		t.Fatalf("materialized title wrong: %q", row.Title)
	}
	if row.SourceKind != store.TaskSourcePeerImport {
		t.Fatalf("materialized source_kind: %q, want %q",
			row.SourceKind, store.TaskSourcePeerImport)
	}
	if row.OriginPeerID != "peer-A" {
		t.Fatalf("origin_peer_id: %q, want peer-A", row.OriginPeerID)
	}
	if row.HlcAt != remoteHLC {
		t.Fatalf("materialized HLC: got %q, want %q", row.HlcAt, remoteHLC)
	}
}

// TestApplyRemoteEvent_CrossWorkspaceDropped pins the safety rule:
// when a remote event references a task that already exists locally
// in a DIFFERENT workspace, we drop the event (no cross-workspace
// migration; the peer probably has a stale binding).
func TestApplyRemoteEvent_CrossWorkspaceDropped(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "keep")
	// Event claims a different workspace_id but same task id — must drop.
	evt := buildEvent(t, row.ID, "ws-remote-mismatch", clock.Now(), "peer-A",
		tasks.RemoteTaskPatch{Title: "should not apply"})
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply cross-ws: %v", err)
	}
	after, _ := svc.Get(ctx, wsID, row.ID)
	if after.Title == "should not apply" {
		t.Fatalf("cross-workspace event leaked through; title got %q", after.Title)
	}
}

// TestApplyRemoteEvent_MissingFieldsRejected confirms wire safety:
// an event with no task_id / no hlc / no workspace_id returns an
// error and doesn't touch the store.
func TestApplyRemoteEvent_MissingFieldsRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newGossipTestSvc(t)
	cases := []p2p.TaskSyncEvent{
		{HLC: "x", WorkspaceID: "ws"},
		{TaskID: "t", WorkspaceID: "ws"},
		{TaskID: "t", HLC: "x"},
	}
	for i, evt := range cases {
		if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err == nil {
			t.Errorf("case %d: expected error for missing fields", i)
		}
	}
}

// TestApplyRemoteEvent_ObservesRemoteHLC pins the receive-rule fix: a
// peer whose wall clock runs ahead must NOT permanently win conflicts.
// After applying its event, our next LOCAL mutation has to stamp an HLC
// strictly greater than the remote stamp (clock.Observe merged it), so
// the local edit wins the next LWW round instead of being dropped as
// stale.
func TestApplyRemoteEvent_ObservesRemoteHLC(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "local title")
	// Remote peer's wall clock is one hour ahead of ours.
	wall, _, err := clock.Parse(clock.Now())
	if err != nil {
		t.Fatalf("parse local stamp: %v", err)
	}
	fastHLC := clock.Format(wall+3_600_000, 0)
	evt := buildEvent(t, row.ID, wsID, fastHLC, "peer-fast", tasks.RemoteTaskPatch{
		Title: "fast peer wrote this",
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-fast", evt); err != nil {
		t.Fatalf("apply fast-clock event: %v", err)
	}
	// Local edit AFTER the remote apply — must stamp past the remote HLC.
	newTitle := "local edit after observe"
	after, err := svc.Update(ctx, wsID, row.ID, tasks.UpdatePatch{
		Title:              &newTitle,
		UpdatedBySessionID: "local-session",
	})
	if err != nil {
		t.Fatalf("local update: %v", err)
	}
	if after.HlcAt <= fastHLC {
		t.Fatalf("local mutation HLC %q <= remote %q — clock.Observe not applied; fast peer permanently wins LWW",
			after.HlcAt, fastHLC)
	}
}

// TestBuildLocalEventForGossip pins the encode side so producer +
// consumer can't drift. Verifies every field on the patch is set + the
// envelope carries task_id + hlc + by_peer.
func TestBuildLocalEventForGossip(t *testing.T) {
	row := &store.Task{
		ID:                 "task-abc",
		WorkspaceID:        "ws-1",
		Title:              "hello",
		Description:        "body",
		Status:             "doing",
		Priority:           "high",
		Meta:               "key: value",
		TagsJSON:           json.RawMessage(`["one"]`),
		AssigneeSessionID:  "sess-1",
		AssigneePeerID:     "peer-zz",
		AssigneeOriginKind: store.TaskAssigneePeer,
		OriginPeerID:       "peer-zz",
		HlcAt:              "0001",
		UpdatedBySessionID: "sess-1",
	}
	evt := tasks.BuildLocalEventForGossip(row, "self-peer")
	if evt.TaskID != "task-abc" || evt.HLC != "0001" {
		t.Fatalf("envelope wrong: %+v", evt)
	}
	if evt.ByPeer != "peer-zz" {
		t.Fatalf("by_peer = %q, want peer-zz", evt.ByPeer)
	}
	var patch tasks.RemoteTaskPatch
	if err := json.Unmarshal(evt.FieldPatchesJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch.Title != "hello" || patch.Status != "doing" || patch.Priority != "high" {
		t.Fatalf("patch fields mismatch: %+v", patch)
	}
	if string(patch.TagsJSON) != `["one"]` {
		t.Fatalf("tags didn't round-trip: %q", string(patch.TagsJSON))
	}
}

// TestBuildLocalEventForGossip_Clears pins that a task with cleared
// fields produces a patch with Clears populated.
func TestBuildLocalEventForGossip_Clears(t *testing.T) {
	row := &store.Task{
		ID:                "task-clear-test",
		WorkspaceID:       "ws-1",
		Title:             "clear me",
		Status:            "open",
		AssigneeSessionID: "",
		AssigneePeerID:    "",
		DueAt:             nil,
		Meta:              "",
		Description:       "",
	}
	evt := tasks.BuildLocalEventForGossip(row, "self-peer")
	var patch tasks.RemoteTaskPatch
	if err := json.Unmarshal(evt.FieldPatchesJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	found := map[string]bool{}
	for _, c := range patch.Clears {
		found[c] = true
	}
	if !found["assignee"] {
		t.Fatalf("clears missing assignee: %v", patch.Clears)
	}
	if !found["due_at"] {
		t.Fatalf("clears missing due_at: %v", patch.Clears)
	}
	if !found["meta"] {
		t.Fatalf("clears missing meta: %v", patch.Clears)
	}
	if !found["description"] {
		t.Fatalf("clears missing description: %v", patch.Clears)
	}
}

func TestBuildLocalEventForGossip_PreservesHumanAssignee(t *testing.T) {
	row := &store.Task{
		ID: "task-human", WorkspaceID: "ws-1", Title: "review",
		AssigneeOriginKind: store.TaskAssigneeHuman,
		AssigneeUserID:     "user-123",
		HlcAt:              "0001",
	}
	evt := tasks.BuildLocalEventForGossip(row, "self-peer")
	var patch tasks.RemoteTaskPatch
	if err := json.Unmarshal(evt.FieldPatchesJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch.AssigneeOriginKind != store.TaskAssigneeHuman || patch.AssigneeUserID != "user-123" {
		t.Fatalf("human assignee did not round-trip: %+v", patch)
	}
	for _, clear := range patch.Clears {
		if clear == "assignee" {
			t.Fatalf("human assignee was incorrectly cleared: %+v", patch)
		}
	}
}

func TestApplyRemoteEvent_ReplacesPeerAssigneeWithHuman(t *testing.T) {
	ctx := context.Background()
	svc, d, wsID := newGossipTestSvc(t)
	row := seedTask(t, svc, wsID, "assigned task")
	row.AssigneeSessionID = "sess-old"
	row.AssigneePeerID = "peer-old"
	row.AssigneeOriginKind = store.TaskAssigneePeer
	if err := d.UpdateTask(ctx, row); err != nil {
		t.Fatalf("seed peer assignee: %v", err)
	}
	evt := buildEvent(t, row.ID, wsID, clock.Now(), "peer-A", tasks.RemoteTaskPatch{
		AssigneeOriginKind: store.TaskAssigneeHuman,
		AssigneeUserID:     "user-123",
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply human assignee: %v", err)
	}
	after, err := svc.Get(ctx, wsID, row.ID)
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if after.AssigneeOriginKind != store.TaskAssigneeHuman || after.AssigneeUserID != "user-123" {
		t.Fatalf("human assignee not applied: %+v", after)
	}
	if after.AssigneeSessionID != "" || after.AssigneePeerID != "" {
		t.Fatalf("stale peer assignee survived human replacement: %+v", after)
	}
}

// TestApplyRemoteEvent_ClearAssignee verifies that a remote event with
// cleared assignee clears the local assignee fields.
func TestApplyRemoteEvent_ClearAssignee(t *testing.T) {
	ctx := context.Background()
	svc, d, wsID := newGossipTestSvc(t)

	row := seedTask(t, svc, wsID, "assigned task")
	row.AssigneeSessionID = "sess-old"
	row.AssigneePeerID = "peer-old"
	row.AssigneeOriginKind = store.TaskAssigneePeer
	if err := d.UpdateTask(ctx, row); err != nil {
		t.Fatalf("seed assignee: %v", err)
	}
	reloaded, _ := svc.Get(ctx, wsID, row.ID)
	if reloaded.AssigneeSessionID != "sess-old" {
		t.Fatalf("assignee not seeded: %q", reloaded.AssigneeSessionID)
	}
	newerHLC := clock.Now()
	evt := buildEvent(t, row.ID, wsID, newerHLC, "peer-A", tasks.RemoteTaskPatch{
		Clears: []string{"assignee"},
	})
	if err := svc.ApplyRemoteEvent(ctx, "peer-A", evt); err != nil {
		t.Fatalf("apply clear assignee: %v", err)
	}
	after, _ := svc.Get(ctx, wsID, row.ID)
	if after.AssigneeSessionID != "" || after.AssigneePeerID != "" {
		t.Fatalf("assignee not cleared: session=%q peer=%q",
			after.AssigneeSessionID, after.AssigneePeerID)
	}
}
