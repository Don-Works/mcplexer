// dashboard_activity_handler_test.go — integration tests for the
// rolling cross-workspace activity feeds backing the dashboard tiles.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newActivityTestServer wires both the tasks and memory services into
// a fresh sqlite-backed router. Returns the server + raw db + svcs so
// tests can seed rows and assert behavior.
func newActivityTestServer(t *testing.T) (*httptest.Server, *sqlite.DB, *tasks.Service, *memory.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tasksSvc := tasks.New(db)
	memSvc := memory.NewService(db, memory.NoopEmbedder{}, nil)
	r := NewRouter(RouterDeps{
		APIToken:  "",
		Store:     db,
		TasksSvc:  tasksSvc,
		MemorySvc: memSvc,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, tasksSvc, memSvc
}

func TestDashboardActivityTasksCrossWorkspace(t *testing.T) {
	srv, db, _, _ := newActivityTestServer(t)
	ctx := context.Background()

	// Two workspaces, two tasks each at staggered timestamps so the
	// ORDER BY updated_at DESC is observable.
	wsA := &store.Workspace{Name: "ws-alpha", RootPath: "/tmp/ws-alpha", Tags: json.RawMessage("[]")}
	wsB := &store.Workspace{Name: "ws-beta", RootPath: "/tmp/ws-beta", Tags: json.RawMessage("[]")}
	for _, ws := range []*store.Workspace{wsA, wsB} {
		if err := db.CreateWorkspace(ctx, ws); err != nil {
			t.Fatalf("create workspace %s: %v", ws.Name, err)
		}
	}

	base := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)
	seed := []struct {
		ws    *store.Workspace
		title string
		ts    time.Time
		hist  []store.TaskStatusHistoryEntry
	}{
		{wsA, "alpha task 1", base, []store.TaskStatusHistoryEntry{{At: base, Evt: "created", To: "open"}}},
		{wsA, "alpha task 2", base.Add(10 * time.Minute), []store.TaskStatusHistoryEntry{
			{At: base, Evt: "created", To: "open"},
			{At: base.Add(10 * time.Minute), Evt: "status_changed", From: "open", To: "doing"},
		}},
		{wsB, "beta task 1", base.Add(20 * time.Minute), []store.TaskStatusHistoryEntry{
			{At: base.Add(20 * time.Minute), Evt: "created", To: "open"},
		}},
		{wsB, "beta task 2", base.Add(30 * time.Minute), []store.TaskStatusHistoryEntry{
			{At: base, Evt: "created", To: "open"},
			{At: base.Add(30 * time.Minute), Evt: "assigned", To: "session-x"},
		}},
	}
	for _, s := range seed {
		hj, _ := json.Marshal(s.hist)
		row := &store.Task{
			WorkspaceID:       s.ws.ID,
			Title:             s.title,
			Status:            "open",
			Priority:          "normal",
			CreatedAt:         s.ts,
			UpdatedAt:         s.ts,
			StatusHistoryJSON: hj,
		}
		if err := db.CreateTask(ctx, row); err != nil {
			t.Fatalf("create task %s: %v", s.title, err)
		}
	}

	resp, err := http.Get(srv.URL + "/api/v1/dashboard/activity/tasks")
	if err != nil {
		t.Fatalf("GET activity/tasks: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var out taskActivityResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tasks) != 4 {
		t.Fatalf("expected 4 task rows, got %d", len(out.Tasks))
	}

	// Newest-first ordering: beta task 2 → beta task 1 → alpha task 2 → alpha task 1.
	titlesInOrder := []string{"beta task 2", "beta task 1", "alpha task 2", "alpha task 1"}
	for i, want := range titlesInOrder {
		if out.Tasks[i].Title != want {
			t.Errorf("position %d: want %q, got %q", i, want, out.Tasks[i].Title)
		}
	}

	// Workspace names + ids are projected onto every row.
	gotWorkspaces := map[string]bool{}
	for _, row := range out.Tasks {
		if row.WorkspaceID == "" || row.WorkspaceName == "" {
			t.Errorf("row %s: missing workspace metadata", row.Title)
		}
		gotWorkspaces[row.WorkspaceName] = true
	}
	if !gotWorkspaces["ws-alpha"] || !gotWorkspaces["ws-beta"] {
		t.Errorf("expected both workspaces in result, got %v", gotWorkspaces)
	}

	// last_event reflects the tail of status_history.
	for _, row := range out.Tasks {
		switch row.Title {
		case "alpha task 1", "beta task 1":
			if row.LastEvent != "created" {
				t.Errorf("%s: want last_event=created, got %q", row.Title, row.LastEvent)
			}
		case "alpha task 2":
			if row.LastEvent != "status_changed" {
				t.Errorf("alpha task 2: want last_event=status_changed, got %q", row.LastEvent)
			}
		case "beta task 2":
			if row.LastEvent != "assigned" {
				t.Errorf("beta task 2: want last_event=assigned, got %q", row.LastEvent)
			}
		}
	}
}

func TestDashboardActivityTasksLimit(t *testing.T) {
	srv, db, _, _ := newActivityTestServer(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "limit-ws", RootPath: "/tmp/limit-ws", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	for i := 0; i < 25; i++ {
		row := &store.Task{
			WorkspaceID: ws.ID,
			Title:       "t",
			Status:      "open",
		}
		if err := db.CreateTask(ctx, row); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Default limit (20).
	resp, err := http.Get(srv.URL + "/api/v1/dashboard/activity/tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out taskActivityResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tasks) != 20 {
		t.Errorf("default limit: want 20 rows, got %d", len(out.Tasks))
	}

	// Explicit limit honored, hard cap at 50.
	resp2, err := http.Get(srv.URL + "/api/v1/dashboard/activity/tasks?limit=5")
	if err != nil {
		t.Fatalf("GET limit=5: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	var out2 taskActivityResponse
	if err := json.NewDecoder(resp2.Body).Decode(&out2); err != nil {
		t.Fatalf("decode limit=5: %v", err)
	}
	if len(out2.Tasks) != 5 {
		t.Errorf("limit=5: want 5 rows, got %d", len(out2.Tasks))
	}
}

func TestDashboardActivityMemoriesShape(t *testing.T) {
	srv, db, _, memSvc := newActivityTestServer(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "mem-ws", RootPath: "/tmp/mem-ws", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create ws: %v", err)
	}
	wsID := ws.ID

	// Two memories: one global, one workspace-scoped. The global one
	// goes in second so it shows up first in the descending-time list.
	if _, err := memSvc.Write(ctx, memory.WriteOptions{
		Name:        "vercel-deploy-rules",
		Content:     "Always run vercel pull before any local dev. The deploy script builds from local tree.",
		Kind:        "fact",
		WorkspaceID: &wsID,
		SourceKind:  store.MemorySourceAgent,
	}); err != nil {
		t.Fatalf("write workspace memory: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := memSvc.Write(ctx, memory.WriteOptions{
		Name:       "global-fact",
		Content:    "Single sentence. Second sentence is ignored.",
		Kind:       "note",
		SourceKind: store.MemorySourceAgent,
	}); err != nil {
		t.Fatalf("write global memory: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/v1/dashboard/activity/memories")
	if err != nil {
		t.Fatalf("GET activity/memories: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var out memoryActivityResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Memories) != 2 {
		t.Fatalf("expected 2 memory rows, got %d", len(out.Memories))
	}

	first := out.Memories[0]
	if first.Name != "global-fact" {
		t.Errorf("expected newest=global-fact first, got %q", first.Name)
	}
	if first.ScopeLabel != "global" {
		t.Errorf("global memory: scope_label=global, got %q", first.ScopeLabel)
	}
	if first.Summary != "Single sentence." {
		t.Errorf("first-sentence projection: want %q, got %q", "Single sentence.", first.Summary)
	}
	if first.Body == "" {
		t.Errorf("body should be populated for expand affordance")
	}

	second := out.Memories[1]
	if second.ScopeLabel != "mem-ws" {
		t.Errorf("workspace memory: scope_label=mem-ws, got %q", second.ScopeLabel)
	}
	if second.WorkspaceID == "" {
		t.Errorf("workspace memory missing workspace_id")
	}
}

func TestFirstSentenceProjection(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Hello world.", "Hello world."},
		{"Hello world. More text.", "Hello world."},
		{"No terminator here just words", "No terminator here just words"},
		{"# Heading\nWith content following.", "Heading\nWith content following."},
		{"", ""},
	}
	for _, c := range cases {
		got := firstSentence(c.in, 140)
		if got != c.want {
			t.Errorf("firstSentence(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
