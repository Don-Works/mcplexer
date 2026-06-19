package runner_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// fakePeerTiers reports a fixed answer about same-user peer presence.
// Mirrors runner.SameUserPeerLister with a count so assertions can
// verify the runner asked at all (the contract is "ask before broadcasting").
type fakePeerTiers struct {
	mu      sync.Mutex
	has     bool
	queries int
}

func (f *fakePeerTiers) HasSameUserPeer(_ context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	return f.has
}

// runConsolidatorTest stands up a runner with the consolidator-shaped
// worker (Name="memory-consolidator") and runs one pass that emits the
// declared number of memory__save dispatches followed by a clean
// end-turn. Returns the audit + mesh observables so the table tests can
// assert on the consolidator finalize signals.
func runConsolidatorTest(
	t *testing.T, saves int, tiers runner.SameUserPeerLister, selfDisplay string,
) (*fakeAuditor, *fakeMesh, string) {
	t.Helper()
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.Name = "memory-consolidator"
	w.ToolAllowlistJSON = `["memory__save","memory__invalidate"]`
	createWorker(t, db, w)

	// Build a response sequence: `saves` tool calls invoking memory__save
	// followed by a clean end-turn so the loop terminates with success.
	resps := make([]models.SendResponse, 0, saves+1)
	for i := 0; i < saves; i++ {
		resps = append(resps, models.SendResponse{
			ToolCalls: []models.ToolCall{{
				ID:   "save_" + itoa(i),
				Name: "memory__save",
				Input: map[string]any{
					"name":    "cluster-" + itoa(i),
					"content": "merged note " + itoa(i),
				},
			}},
			StopReason: models.StopToolUse,
		})
	}
	resps = append(resps, models.SendResponse{Text: "done", StopReason: models.StopEndTurn})
	adapter := &fakeAdapter{responses: resps}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"memory__save": {OutputJSON: `{"id":"mem_new"}`, WriteClass: true},
	}}
	mesh := &fakeMesh{}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:       db,
		Dispatcher:  disp,
		Mesh:        mesh,
		Secrets:     &fakeSecrets{},
		Auditor:     auditor,
		Adapter:     func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		PeerTiers:   tiers,
		SelfDisplay: selfDisplay,
	})
	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return auditor, mesh, runID
}

// itoa is a 1-digit-friendly stringifier used by the test rig so
// the test file stays free of strconv import noise.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestConsolidator_FinalizeEmitsAuditAndBroadcast is the headline
// regression: a successful consolidator run with N memory__save
// dispatches and a Tier-1 peer present produces ONE memory__consolidator_run
// audit row (with the right params) AND ONE finding-kind mesh broadcast.
func TestConsolidator_FinalizeEmitsAuditAndBroadcast(t *testing.T) {
	tiers := &fakePeerTiers{has: true}
	auditor, mesh, runID := runConsolidatorTest(t, 3, tiers, "alice@m1")

	// 1. Audit row present with the right shape.
	auditor.mu.Lock()
	var found *store.AuditRecord
	for _, rec := range auditor.records {
		if rec.ToolName == "memory__consolidator_run" {
			found = rec
			break
		}
	}
	auditor.mu.Unlock()
	if found == nil {
		t.Fatalf("expected memory__consolidator_run audit row; got names=%v", auditor.toolNames())
	}
	var params map[string]any
	if err := json.Unmarshal(found.ParamsRedacted, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params["consolidations_performed"] != float64(3) {
		t.Errorf("consolidations_performed = %v, want 3", params["consolidations_performed"])
	}
	if params["run_id"] != runID {
		t.Errorf("run_id = %v, want %s", params["run_id"], runID)
	}
	if params["workspace_id"] == "" {
		t.Errorf("workspace_id is empty")
	}
	if _, ok := params["started_at"].(string); !ok {
		t.Errorf("started_at missing or non-string: %v", params["started_at"])
	}
	if _, ok := params["finished_at"].(string); !ok {
		t.Errorf("finished_at missing or non-string: %v", params["finished_at"])
	}

	// 2. Mesh broadcast present, finding-kind, low-priority, with content
	//    that includes the self-display label and the count.
	mesh.mu.Lock()
	defer mesh.mu.Unlock()
	var broadcast *runner.MeshOutbound
	for i := range mesh.sent {
		m := &mesh.sent[i]
		if m.Kind == "finding" && strings.Contains(m.Content, "ran consolidator") {
			broadcast = m
			break
		}
	}
	if broadcast == nil {
		t.Fatalf("expected finding broadcast; got kinds=%v", meshKindsAndContents(mesh))
	}
	if broadcast.Priority != "low" {
		t.Errorf("priority = %q, want low", broadcast.Priority)
	}
	if !strings.Contains(broadcast.Content, "alice@m1") {
		t.Errorf("content missing self-display: %q", broadcast.Content)
	}
	if !strings.Contains(broadcast.Content, "3 consolidations") {
		t.Errorf("content missing tally: %q", broadcast.Content)
	}
	if !strings.Contains(broadcast.Tags, "memory_consolidated") {
		t.Errorf("tags missing memory_consolidated: %q", broadcast.Tags)
	}
	if !broadcast.BroadcastPeers {
		t.Error("expected same-user peer-gated consolidator broadcast to opt into cross-peer delivery")
	}
	if tiers.queries == 0 {
		t.Errorf("PeerTiers.HasSameUserPeer was never queried")
	}
}

// TestConsolidator_NoBroadcastWhenNoSameUserPeer verifies the Tier-1
// gate: when the lister reports no same-user peer, the mesh broadcast
// MUST be suppressed. The audit row still fires (it's not peer-gated;
// it's a local domain event that downstream readers join on).
func TestConsolidator_NoBroadcastWhenNoSameUserPeer(t *testing.T) {
	tiers := &fakePeerTiers{has: false}
	auditor, mesh, _ := runConsolidatorTest(t, 2, tiers, "bob@m2")

	// Audit row still fires.
	auditor.mu.Lock()
	sawAudit := false
	for _, rec := range auditor.records {
		if rec.ToolName == "memory__consolidator_run" {
			sawAudit = true
			break
		}
	}
	auditor.mu.Unlock()
	if !sawAudit {
		t.Fatalf("memory__consolidator_run audit must fire even without Tier-1 peer; got %v", auditor.toolNames())
	}

	// Mesh broadcast MUST NOT fire.
	mesh.mu.Lock()
	defer mesh.mu.Unlock()
	for _, m := range mesh.sent {
		if m.Kind == "finding" && strings.Contains(m.Content, "ran consolidator") {
			t.Fatalf("expected no finding broadcast; got: %+v", m)
		}
	}
	if tiers.queries == 0 {
		t.Errorf("PeerTiers.HasSameUserPeer was never queried")
	}
}

// TestConsolidator_BroadcastWhenNilLister verifies the no-lister-wired
// path (tests + single-machine deployments): when PeerTiers is nil, the
// broadcast fires unconditionally so the provenance row exists locally.
func TestConsolidator_BroadcastWhenNilLister(t *testing.T) {
	_, mesh, _ := runConsolidatorTest(t, 1, nil, "user@dev")
	mesh.mu.Lock()
	defer mesh.mu.Unlock()
	var broadcast *runner.MeshOutbound
	for _, m := range mesh.sent {
		if m.Kind == "finding" && strings.Contains(m.Content, "ran consolidator") {
			broadcast = &m
			break
		}
	}
	if broadcast == nil {
		t.Fatalf("expected broadcast when PeerTiers nil; got kinds=%v", meshKindsAndContents(mesh))
	}
	if broadcast.BroadcastPeers {
		t.Error("nil PeerTiers path should emit local provenance only, not cross-peer delivery")
	}
}

// TestConsolidator_NonConsolidatorWorkerSkipsHooks verifies the worker-
// name guard: a non-"memory-consolidator" worker that happens to call
// memory__save MUST NOT emit the consolidator audit or broadcast (the
// hooks are name-gated, not action-gated).
func TestConsolidator_NonConsolidatorWorkerSkipsHooks(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.Name = "some-other-worker"
	w.ToolAllowlistJSON = `["memory__save"]`
	createWorker(t, db, w)

	resps := []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "s1", Name: "memory__save"}}, StopReason: models.StopToolUse},
		{Text: "done", StopReason: models.StopEndTurn},
	}
	adapter := &fakeAdapter{responses: resps}
	disp := &fakeDispatcher{results: map[string]runner.ToolCallResult{
		"memory__save": {OutputJSON: `{"id":"x"}`},
	}}
	mesh := &fakeMesh{}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       mesh,
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
		// PeerTiers nil — broadcast would fire if name gate didn't.
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range auditor.toolNames() {
		if name == "memory__consolidator_run" {
			t.Fatalf("non-consolidator worker emitted memory__consolidator_run audit")
		}
	}
	mesh.mu.Lock()
	defer mesh.mu.Unlock()
	for _, m := range mesh.sent {
		if m.Kind == "finding" && strings.Contains(m.Content, "ran consolidator") {
			t.Fatalf("non-consolidator worker emitted ran-consolidator broadcast: %+v", m)
		}
	}
}

// TestConsolidator_FailedRunSuppressesHooks verifies the success gate:
// when the consolidator run lands in failure (adapter error), neither
// the audit row nor the broadcast fires — we don't claim "consolidated"
// on a crashed run.
func TestConsolidator_FailedRunSuppressesHooks(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.Name = "memory-consolidator"
	createWorker(t, db, w)

	adapter := &fakeAdapter{err: errSentinelBoom()}
	mesh := &fakeMesh{}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       mesh,
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusFailure {
		t.Fatalf("status = %q, want failure", run.Status)
	}
	for _, name := range auditor.toolNames() {
		if name == "memory__consolidator_run" {
			t.Fatalf("failed consolidator run emitted memory__consolidator_run audit")
		}
	}
	mesh.mu.Lock()
	defer mesh.mu.Unlock()
	for _, m := range mesh.sent {
		if m.Kind == "finding" && strings.Contains(m.Content, "ran consolidator") {
			t.Fatalf("failed consolidator run emitted broadcast: %+v", m)
		}
	}
}

// meshKindsAndContents pretty-prints the fake mesh for diagnostic
// failure messages.
func meshKindsAndContents(m *fakeMesh) []string {
	out := make([]string, 0, len(m.sent))
	for _, x := range m.sent {
		out = append(out, x.Kind+":"+x.Content)
	}
	return out
}
