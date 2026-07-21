// handler_brain_test.go — gateway-level coverage for the agent-facing
// brain__* tools. Verifies the read surface (tree/list/get/search) returns
// records over a real brain Editor, the write path persists a note, and the
// whole family degrades gracefully to a tool-level error when the brain
// subsystem is disabled (nil Editor).
package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newHandlerWithBrain constructs a handler wired to a real brain Editor backed
// by an on-disk SQLite store (real migrations) + a temp brain dir, and seeds a
// "ws" workspace so task/memory rows have a home. Returns the handler, the
// store, and the Editor so a test can seed + assert directly.
func newHandlerWithBrain(t *testing.T) (*handler, store.Store, *brain.Editor) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "ws", Name: "Workspace"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ix := brain.NewIndexer(cfg, db, nil)
	ser := brain.NewSerializer(cfg, db, nil)
	ser.ShareSelfWrites(ix)
	ed := brain.NewEditor(db, ser)

	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.store = db
	h.brainEditor = ed
	h.sessions.wsChain = []routing.WorkspaceAncestor{
		{ID: "ws", Name: "Workspace", RootPath: "/test/ws"},
	}
	return h, db, ed
}

// brainText calls a brain tool and returns the response text, failing on an
// RPC-level error or an unhandled tool.
func brainText(t *testing.T, h *handler, name, args string) string {
	t.Helper()
	return brainTextWithContext(t, h, context.Background(), name, args)
}

func brainTextWithContext(t *testing.T, h *handler, ctx context.Context, name, args string) string {
	t.Helper()
	resp, rpcErr, handled := h.dispatchBrainTool(ctx, name, json.RawMessage(args))
	if !handled {
		t.Fatalf("%s not handled", name)
	}
	if rpcErr != nil {
		t.Fatalf("%s rpcErr: %v", name, rpcErr)
	}
	var parsed CallToolResult
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("%s unmarshal result: %v", name, err)
	}
	if len(parsed.Content) == 0 {
		t.Fatalf("%s empty content", name)
	}
	return parsed.Content[0].Text
}

func TestBrainReadTools_ReturnRecords(t *testing.T) {
	h, _, ed := newHandlerWithBrain(t)
	ctx := context.Background()

	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "Fix the scheduler", Status: "open",
		Description: "Cron jobs fire once.",
	}, nil); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	noteRec, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		Kind: brain.MemoryKindNote, Name: "deploy-runbook", Content: "ship it",
		Workspace: "ws",
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	tc := []struct {
		name string
		tool string
		args string
		want []string
	}{
		{
			name: "tree lists workspace with counts",
			tool: "brain__tree", args: `{}`,
			want: []string{`"workspace": "ws"`, `"task_count": 1`},
		},
		{
			name: "list tasks returns the seeded task",
			tool: "brain__list", args: `{"kind":"task","workspace":"ws"}`,
			want: []string{"Fix the scheduler", `"status": "open"`},
		},
		{
			name: "list memories returns the seeded note",
			tool: "brain__list", args: `{"kind":"memory","workspace":"ws"}`,
			want: []string{"deploy-runbook"},
		},
		{
			name: "get task includes raw md body",
			tool: "brain__get", args: `{"kind":"task","id":"` + taskIDFor(t, ed) + `"}`,
			want: []string{"Fix the scheduler", "Cron jobs fire once."},
		},
		{
			name: "get memory by id",
			tool: "brain__get", args: `{"kind":"memory","id":"` + noteRec.ID + `"}`,
			want: []string{"deploy-runbook", "ship it"},
		},
		{
			name: "search finds task by prefix",
			tool: "brain__search", args: `{"q":"Fix","kind":"task","workspace":"ws"}`,
			want: []string{"Fix the scheduler", `"tier": 0`},
		},
	}
	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			body := brainText(t, h, tt.tool, tt.args)
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("%s: want %q in:\n%s", tt.tool, w, body)
				}
			}
		})
	}
}

func TestBrainTools_WorkerWorkspaceAccessScopesReads(t *testing.T) {
	h, st, ed := newHandlerWithBrain(t)
	ctx := context.Background()
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "other", Name: "Other"}); err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "secret", Name: "Secret"}); err != nil {
		t.Fatalf("create secret workspace: %v", err)
	}
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "Alpha home", Status: "open",
	}, nil); err != nil {
		t.Fatalf("seed home task: %v", err)
	}
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "other", Title: "Alpha other", Status: "open",
	}, nil); err != nil {
		t.Fatalf("seed other task: %v", err)
	}
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "secret", Title: "Alpha secret", Status: "open",
	}, nil); err != nil {
		t.Fatalf("seed secret task: %v", err)
	}

	workerCtx := WithWorkerWorkspaceAccess(ctx, "ws", []WorkerWorkspaceGrant{
		{WorkspaceID: "ws", WorkspaceName: "Workspace", Access: store.WorkerWorkspaceAccessWrite},
		{WorkspaceID: "other", WorkspaceName: "Other", Access: store.WorkerWorkspaceAccessRead},
	})

	tree := brainTextWithContext(t, h, workerCtx, "brain__tree", `{}`)
	if !strings.Contains(tree, `"workspace": "ws"`) || !strings.Contains(tree, `"workspace": "other"`) {
		t.Fatalf("tree missing granted workspaces:\n%s", tree)
	}
	if strings.Contains(tree, `"workspace": "secret"`) {
		t.Fatalf("tree leaked ungranted workspace:\n%s", tree)
	}

	searchDefault := brainTextWithContext(t, h, workerCtx, "brain__search", `{"q":"Alpha","kind":"task"}`)
	if !strings.Contains(searchDefault, "Alpha home") {
		t.Fatalf("default worker search missing preferred workspace task:\n%s", searchDefault)
	}
	if strings.Contains(searchDefault, "Alpha other") || strings.Contains(searchDefault, "Alpha secret") {
		t.Fatalf("default worker search leaked non-preferred workspace:\n%s", searchDefault)
	}

	_, rpcErr, handled := h.dispatchBrainTool(
		workerCtx,
		"brain__search",
		json.RawMessage(`{"q":"Alpha","kind":"task","workspace":"secret"}`),
	)
	if !handled {
		t.Fatal("brain__search not handled")
	}
	if rpcErr == nil {
		t.Fatal("worker search of ungranted workspace allowed")
	}
}

func TestBrainTools_SessionWorkspaceScopesReads(t *testing.T) {
	h, st, ed := newHandlerWithBrain(t)
	ctx := context.Background()
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "secret", Name: "Secret"}); err != nil {
		t.Fatalf("create secret workspace: %v", err)
	}
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "Alpha home", Status: "open",
	}, nil); err != nil {
		t.Fatalf("seed home task: %v", err)
	}
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "secret", Title: "Alpha secret", Status: "open",
	}, nil); err != nil {
		t.Fatalf("seed secret task: %v", err)
	}

	tree := brainText(t, h, "brain__tree", `{}`)
	if !strings.Contains(tree, `"workspace": "ws"`) {
		t.Fatalf("tree missing session workspace:\n%s", tree)
	}
	if strings.Contains(tree, `"workspace": "secret"`) {
		t.Fatalf("tree leaked out-of-session workspace:\n%s", tree)
	}

	searchDefault := brainText(t, h, "brain__search", `{"q":"Alpha","kind":"task"}`)
	if !strings.Contains(searchDefault, "Alpha home") {
		t.Fatalf("default search missing session workspace task:\n%s", searchDefault)
	}
	if strings.Contains(searchDefault, "Alpha secret") {
		t.Fatalf("default search leaked out-of-session workspace:\n%s", searchDefault)
	}

	_, rpcErr, handled := h.dispatchBrainTool(
		ctx,
		"brain__list",
		json.RawMessage(`{"kind":"task","workspace":"secret"}`),
	)
	if !handled {
		t.Fatal("brain__list not handled")
	}
	if rpcErr == nil {
		t.Fatal("session list of out-of-scope workspace allowed")
	}
}

func TestBrainPeople_WorkerWorkspaceAccessScopesCRM(t *testing.T) {
	h, _, ed := newHandlerWithBrain(t)
	ctx := context.Background()
	person, err := ed.SavePerson(ctx, brain.PersonRecord{
		Workspace: store.PersonDefaultWorkspaceID,
		Name:      "Katherine Johnson",
		Company:   "NASA",
		Notes:     "Orbital mechanics.",
	})
	if err != nil {
		t.Fatalf("seed person: %v", err)
	}

	readCRM := WithWorkerWorkspaceAccess(ctx, "ws", []WorkerWorkspaceGrant{
		{WorkspaceID: "ws", WorkspaceName: "Workspace", Access: store.WorkerWorkspaceAccessWrite},
		{WorkspaceID: store.PersonDefaultWorkspaceID, WorkspaceName: "CRM", Access: store.WorkerWorkspaceAccessRead},
	})
	list := brainTextWithContext(t, h, readCRM, "brain__list_people", `{}`)
	if !strings.Contains(list, "Katherine Johnson") {
		t.Fatalf("read-granted worker did not see CRM person:\n%s", list)
	}
	get := brainTextWithContext(t, h, readCRM, "brain__get_person", `{"id":"`+person.ID+`"}`)
	if !strings.Contains(get, "Orbital mechanics.") {
		t.Fatalf("read-granted worker could not get CRM person:\n%s", get)
	}
	_, rpcErr, handled := h.dispatchBrainTool(
		readCRM,
		"brain__write_person",
		json.RawMessage(`{"name":"Hidden Write"}`),
	)
	if !handled {
		t.Fatal("brain__write_person not handled")
	}
	if rpcErr == nil {
		t.Fatal("read-only CRM worker was allowed to write a person")
	}

	noCRM := WithWorkerWorkspaceAccess(ctx, "ws", []WorkerWorkspaceGrant{
		{WorkspaceID: "ws", WorkspaceName: "Workspace", Access: store.WorkerWorkspaceAccessWrite},
	})
	_, rpcErr, handled = h.dispatchBrainTool(noCRM, "brain__list_people", json.RawMessage(`{}`))
	if !handled {
		t.Fatal("brain__list_people not handled")
	}
	if rpcErr == nil {
		t.Fatal("worker without CRM access was allowed to list people")
	}
}

// taskIDFor returns the id of the single seeded "ws" task so a table row can
// reference it without copy-pasting a minted ULID.
func taskIDFor(t *testing.T, ed *brain.Editor) string {
	t.Helper()
	rows, err := ed.ListTasks(context.Background(), "ws")
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListTasks for id lookup: %v (rows=%d)", err, len(rows))
	}
	return rows[0].ID
}

func TestBrainWriteNote_PersistsRecord(t *testing.T) {
	h, _, ed := newHandlerWithBrain(t)

	body := brainText(t, h, "brain__write_note",
		`{"name":"meeting-notes","content":"# Standup\n- shipped brain tools","workspace":"ws","tags":["standup"]}`)
	if !strings.Contains(body, `"saved": true`) {
		t.Fatalf("expected saved=true, got: %s", body)
	}

	var res struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("unmarshal write result: %v", err)
	}
	if res.ID == "" {
		t.Fatal("expected a minted note id")
	}
	// The .md file is on disk (write went through the canonical Serializer).
	if res.Path == "" {
		t.Fatal("expected a resolved .md path")
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("expected note file at %s: %v", res.Path, err)
	}
	// The record is readable back through the Editor.
	got, err := ed.GetMemory(context.Background(), res.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Name != "meeting-notes" || got.Kind != brain.MemoryKindNote {
		t.Fatalf("record mismatch: %+v", got)
	}
}

func TestBrainWritePerson_PersistsAndReadsBack(t *testing.T) {
	h, _, ed := newHandlerWithBrain(t)

	writeBody := brainText(t, h, "brain__write_person",
		`{"name":"Margaret Hamilton","email":"margaret@nasa.gov","company":"NASA","role":"Lead Engineer","notes":"Wrote Apollo flight software.","tags":["apollo"],"entities":[{"kind":"org","id":"nasa"}]}`)
	if !strings.Contains(writeBody, `"saved": true`) {
		t.Fatalf("expected saved=true, got: %s", writeBody)
	}
	var written struct {
		ID        string `json:"id"`
		Path      string `json:"path"`
		Workspace string `json:"workspace"`
	}
	if err := json.Unmarshal([]byte(writeBody), &written); err != nil {
		t.Fatalf("unmarshal write result: %v", err)
	}
	if written.ID == "" {
		t.Fatal("expected a minted person id")
	}
	// The .md file is on disk (write went through the canonical Serializer).
	if written.Workspace != store.PersonDefaultWorkspaceID {
		t.Fatalf("expected crm workspace, got %q", written.Workspace)
	}
	if written.Path == "" || !strings.Contains(written.Path, filepath.Join("workspaces", "crm", "crm", "people")) {
		t.Fatalf("expected a workspaces/crm/crm/people path, got %q", written.Path)
	}
	if _, err := os.Stat(written.Path); err != nil {
		t.Fatalf("expected person file at %s: %v", written.Path, err)
	}

	// brain__get_person reads it back, including the raw .md + entity links.
	getBody := brainText(t, h, "brain__get_person", `{"id":"`+written.ID+`"}`)
	for _, want := range []string{"Margaret Hamilton", "NASA", "Wrote Apollo flight software.", `"Kind": "org"`} {
		if !strings.Contains(getBody, want) {
			t.Errorf("brain__get_person: want %q in:\n%s", want, getBody)
		}
	}

	// brain__list_people surfaces the record.
	listBody := brainText(t, h, "brain__list_people", `{}`)
	if !strings.Contains(listBody, "Margaret Hamilton") {
		t.Errorf("brain__list_people: missing record:\n%s", listBody)
	}

	// The record is readable through the Editor directly.
	got, err := ed.GetPerson(context.Background(), written.ID)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if got.Name != "Margaret Hamilton" || got.Company != "NASA" {
		t.Fatalf("record mismatch: %+v", got)
	}
}

func TestBrainTools_NilEditorDegradesGracefully(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, _ := newTestHandler(lister, nil)
	h.brainEditor = nil // brain subsystem disabled

	for _, name := range []string{
		"brain__tree", "brain__list", "brain__get", "brain__search", "brain__write_note",
		"brain__list_people", "brain__get_person", "brain__write_person",
	} {
		resp, rpcErr, handled := h.dispatchBrainTool(context.Background(), name, json.RawMessage(`{}`))
		if !handled {
			t.Errorf("%s: expected handled even when disabled", name)
			continue
		}
		if rpcErr != nil {
			t.Errorf("%s: expected tool-level error, got rpcErr=%v", name, rpcErr)
			continue
		}
		var parsed CallToolResult
		if err := json.Unmarshal(resp, &parsed); err != nil {
			t.Errorf("%s: unmarshal: %v", name, err)
			continue
		}
		if !parsed.IsError {
			t.Errorf("%s: expected isError=true when brain disabled", name)
		}
		if len(parsed.Content) == 0 || !strings.Contains(parsed.Content[0].Text, "not enabled") {
			t.Errorf("%s: expected 'not enabled' message, got: %s", name, resp)
		}
	}
}
