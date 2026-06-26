// tasks_handler_test.go — HTTP-level tests for the tasks REST surface.
// Spins up a real sqlite-backed tasks.Service so each test exercises the
// same code path the PWA hits in production.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newTasksTestServer wires a fresh sqlite-backed tasks.Service into an
// httptest.Server. Returns the server + the underlying store so tests
// can seed rows or assert side-effects directly.
func newTasksTestServer(t *testing.T) (*httptest.Server, *sqlite.DB, *tasks.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	svc := tasks.New(db)
	r := NewRouter(RouterDeps{
		APIToken: "",
		Store:    db,
		TasksSvc: svc,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, svc
}

func TestListMilestonesEndpointShape(t *testing.T) {
	srv, db, _ := newTasksTestServer(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "ws-milestones", RootPath: "/tmp/ws-milestones", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Seed a milestone epic (due in 3 days, created 4 days ago) + two
	// children, one already closed.
	createdAt := time.Now().Add(-4 * 24 * time.Hour).UTC().Truncate(time.Second)
	dueAt := time.Now().Add(3 * 24 * time.Hour).UTC().Truncate(time.Second)
	epic := &store.Task{
		WorkspaceID: ws.ID,
		Title:       "Milestone A",
		TagsJSON:    json.RawMessage(`["milestone"]`),
		DueAt:       &dueAt,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if err := db.CreateTask(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}
	childA := &store.Task{WorkspaceID: ws.ID, Title: "child A"}
	childB := &store.Task{WorkspaceID: ws.ID, Title: "child B"}
	if err := db.CreateTask(ctx, childA); err != nil {
		t.Fatalf("create child A: %v", err)
	}
	if err := db.CreateTask(ctx, childB); err != nil {
		t.Fatalf("create child B: %v", err)
	}
	closeAt := createdAt.Add(2 * 24 * time.Hour)
	childA.ClosedAt = &closeAt
	childA.Status = "done"
	if err := db.UpdateTask(ctx, childA); err != nil {
		t.Fatalf("close child A: %v", err)
	}
	epic.Meta = "composes: " + childA.ID + ", " + childB.ID
	if err := db.UpdateTask(ctx, epic); err != nil {
		t.Fatalf("update epic meta: %v", err)
	}

	// 400 when workspace_id is missing.
	resp, err := http.Get(srv.URL + "/api/v1/tasks/milestones")
	if err != nil {
		t.Fatalf("get without ws: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without workspace_id, got %d", resp.StatusCode)
	}

	// 200 + correctly-shaped payload.
	resp2, err := http.Get(srv.URL + "/api/v1/tasks/milestones?workspace_id=" + ws.ID)
	if err != nil {
		t.Fatalf("get milestones: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	var out []store.MilestoneBurndown
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(out))
	}
	mb := out[0]
	if mb.Task.ID != epic.ID {
		t.Errorf("expected milestone id=%s, got %s", epic.ID, mb.Task.ID)
	}
	if mb.TotalChildren != 2 {
		t.Errorf("expected TotalChildren=2, got %d", mb.TotalChildren)
	}
	if mb.ClosedChildren != 1 {
		t.Errorf("expected ClosedChildren=1, got %d", mb.ClosedChildren)
	}
	if len(mb.BurndownPoints) == 0 {
		t.Errorf("expected non-empty burndown series, got 0 points")
	}
	if mb.DaysRemaining < 2 || mb.DaysRemaining > 3 {
		t.Errorf("expected DaysRemaining ~3, got %d", mb.DaysRemaining)
	}

	// Empty list when workspace has no milestones.
	emptyWS := &store.Workspace{Name: "empty-ws", RootPath: "/tmp/empty-ws", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, emptyWS); err != nil {
		t.Fatalf("create empty ws: %v", err)
	}
	resp3, err := http.Get(srv.URL + "/api/v1/tasks/milestones?workspace_id=" + emptyWS.ID)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	defer func() { _ = resp3.Body.Close() }()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on empty ws, got %d", resp3.StatusCode)
	}
	var empty []store.MilestoneBurndown
	if err := json.NewDecoder(resp3.Body).Decode(&empty); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 milestones in empty ws, got %d", len(empty))
	}
}

func TestListTaskStatusesEndpointScopesStateAndWorkspace(t *testing.T) {
	srv, db, _ := newTasksTestServer(t)
	ctx := context.Background()

	wsA := &store.Workspace{Name: "ws-status-a", RootPath: "/tmp/ws-status-a", Tags: json.RawMessage("[]")}
	wsB := &store.Workspace{Name: "ws-status-b", RootPath: "/tmp/ws-status-b", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, wsA); err != nil {
		t.Fatalf("create wsA: %v", err)
	}
	if err := db.CreateWorkspace(ctx, wsB); err != nil {
		t.Fatalf("create wsB: %v", err)
	}
	closedAt := time.Now().UTC()
	for _, row := range []*store.Task{
		{WorkspaceID: wsA.ID, Title: "a1", Status: "triage"},
		{WorkspaceID: wsA.ID, Title: "a2", Status: "triage"},
		{WorkspaceID: wsA.ID, Title: "a3", Status: "coding"},
		{WorkspaceID: wsA.ID, Title: "a4", Status: "done", ClosedAt: &closedAt},
		{WorkspaceID: wsB.ID, Title: "b1", Status: "other"},
	} {
		if err := db.CreateTask(ctx, row); err != nil {
			t.Fatalf("create task %s: %v", row.Title, err)
		}
	}

	resp, err := http.Get(srv.URL + "/api/v1/tasks/statuses?workspace_id=" + wsA.ID + "&state=open")
	if err != nil {
		t.Fatalf("get statuses: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		Statuses []taskStatusCountResponseRow `json:"statuses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]int{}
	for _, row := range out.Statuses {
		got[row.Status] = row.Count
	}
	if got["triage"] != 2 || got["coding"] != 1 || got["done"] != 0 || got["other"] != 0 {
		t.Fatalf("open workspace statuses = %v", got)
	}

	respAll, err := http.Get(srv.URL + "/api/v1/tasks/statuses?state=all")
	if err != nil {
		t.Fatalf("get all statuses: %v", err)
	}
	defer func() { _ = respAll.Body.Close() }()
	var all struct {
		Statuses []taskStatusCountResponseRow `json:"statuses"`
	}
	if err := json.NewDecoder(respAll.Body).Decode(&all); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	allGot := map[string]int{}
	for _, row := range all.Statuses {
		allGot[row.Status] = row.Count
	}
	if allGot["done"] != 1 || allGot["other"] != 1 {
		t.Fatalf("all-workspace statuses = %v, want done and other included", allGot)
	}
}

// TestHumanAssigneeRESTCreateFilter exercises the REST surface for
// human-assigned tasks end-to-end. Mirrors the service-level coverage
// in internal/tasks/service_test.go (migration 105) so the dashboard's
// HTTP path stays in lock-step with the gateway MCP tool.
func TestHumanAssigneeRESTCreateFilter(t *testing.T) {
	srv, db, _ := newTasksTestServer(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "ws-human", RootPath: "/tmp/ws-human", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// POST /api/v1/tasks with assignee.user_id — backend should round-
	// trip the row with assignee_user_id + assignee_origin_kind=human.
	createBody := map[string]any{
		"workspace_id": ws.ID,
		"title":        "Human owned",
		"assignee":     map[string]any{"user_id": "user-rest-1"},
	}
	cb, _ := json.Marshal(createBody)
	resp, err := http.Post(srv.URL+"/api/v1/tasks", "application/json", bytes.NewReader(cb))
	if err != nil {
		t.Fatalf("POST /tasks: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	var created store.Task
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	_ = resp.Body.Close()
	if created.AssigneeUserID != "user-rest-1" {
		t.Errorf("expected assignee_user_id=user-rest-1, got %q", created.AssigneeUserID)
	}
	if created.AssigneeOriginKind != store.TaskAssigneeHuman {
		t.Errorf("expected origin_kind=human, got %q", created.AssigneeOriginKind)
	}

	// Seed a second unassigned row so the filter has something to exclude.
	createBody2 := map[string]any{
		"workspace_id": ws.ID,
		"title":        "Unassigned neighbour",
	}
	cb2, _ := json.Marshal(createBody2)
	resp2, err := http.Post(srv.URL+"/api/v1/tasks", "application/json", bytes.NewReader(cb2))
	if err != nil {
		t.Fatalf("POST /tasks #2: %v", err)
	}
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 on second create, got %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()

	// GET /api/v1/tasks?assignee_user_id=user-rest-1 → exactly the human row.
	listURL := srv.URL + "/api/v1/tasks?workspace_id=" + ws.ID + "&assignee_user_id=user-rest-1"
	resp3, err := http.Get(listURL)
	if err != nil {
		t.Fatalf("GET /tasks filter: %v", err)
	}
	defer func() { _ = resp3.Body.Close() }()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}
	var rows []store.Task
	if err := json.NewDecoder(resp3.Body).Decode(&rows); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].AssigneeUserID != "user-rest-1" {
		t.Errorf("filtered row missing assignee_user_id, got %q", rows[0].AssigneeUserID)
	}

	// GET /api/v1/tasks?assignee_origin_kind=human → same single row.
	resp4, err := http.Get(srv.URL + "/api/v1/tasks?workspace_id=" + ws.ID + "&assignee_origin_kind=human")
	if err != nil {
		t.Fatalf("GET /tasks origin filter: %v", err)
	}
	defer func() { _ = resp4.Body.Close() }()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp4.StatusCode)
	}
	var rowsHuman []store.Task
	if err := json.NewDecoder(resp4.Body).Decode(&rowsHuman); err != nil {
		t.Fatalf("decode origin list: %v", err)
	}
	if len(rowsHuman) != 1 || rowsHuman[0].AssigneeUserID != "user-rest-1" {
		t.Fatalf("origin_kind=human expected 1 row with user, got %d (first=%+v)", len(rowsHuman), rowsHuman)
	}

	// POST /api/v1/tasks/{id}/update with assignee.user_id → re-assigns
	// the second row to a different human, then assert the round-trip.
	patchBody := map[string]any{
		"assignee": map[string]any{"user_id": "user-rest-2"},
	}
	pb, _ := json.Marshal(patchBody)
	updateURL := srv.URL + "/api/v1/tasks/" + created.ID + "/update?workspace_id=" + ws.ID
	req, _ := http.NewRequest(http.MethodPost, updateURL, bytes.NewReader(pb))
	req.Header.Set("Content-Type", "application/json")
	resp5, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /tasks/{id}/update: %v", err)
	}
	defer func() { _ = resp5.Body.Close() }()
	if resp5.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp5.Body)
		t.Fatalf("expected 200 on update, got %d: %s", resp5.StatusCode, string(body))
	}
	var updated store.Task
	if err := json.NewDecoder(resp5.Body).Decode(&updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updated.AssigneeUserID != "user-rest-2" {
		t.Errorf("expected assignee_user_id=user-rest-2 after update, got %q", updated.AssigneeUserID)
	}
	if updated.AssigneeOriginKind != store.TaskAssigneeHuman {
		t.Errorf("expected origin_kind=human after update, got %q", updated.AssigneeOriginKind)
	}

	// clear: ["assignee"] should reset all three identity columns.
	clearBody := map[string]any{"clear": []string{"assignee"}}
	clb, _ := json.Marshal(clearBody)
	req2, _ := http.NewRequest(http.MethodPost, updateURL, bytes.NewReader(clb))
	req2.Header.Set("Content-Type", "application/json")
	resp6, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST clear assignee: %v", err)
	}
	defer func() { _ = resp6.Body.Close() }()
	if resp6.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on clear, got %d", resp6.StatusCode)
	}
	var cleared store.Task
	if err := json.NewDecoder(resp6.Body).Decode(&cleared); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if cleared.AssigneeUserID != "" {
		t.Errorf("expected empty assignee_user_id after clear, got %q", cleared.AssigneeUserID)
	}
	if cleared.AssigneeOriginKind != store.TaskAssigneeLocal {
		t.Errorf("expected origin_kind=local after clear, got %q", cleared.AssigneeOriginKind)
	}
}
