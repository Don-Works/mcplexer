package brain_test

import (
	"errors"
	"os"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// TestEditor_ScopeFusion verifies the agent's literal scope string fuses the
// workspace, its ancestor chain, then global.
func TestEditor_ScopeFusion(t *testing.T) {
	ed, st, _, ctx := newEditor(t)
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("create client ws: %v", err)
	}
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "acme-api", Name: "Acme API", ParentID: "acme"}); err != nil {
		t.Fatalf("create child ws: %v", err)
	}

	cases := []struct {
		ws   string
		want string
	}{
		{"acme-api", "acme-api ∪ acme ∪ global"},
		{"acme", "acme ∪ global"},
		{"global", "global"},
		{"", "global"},
	}
	for _, tc := range cases {
		got, err := ed.Scope(ctx, tc.ws)
		if err != nil {
			t.Fatalf("Scope(%q): %v", tc.ws, err)
		}
		if got != tc.want {
			t.Fatalf("Scope(%q) = %q, want %q", tc.ws, got, tc.want)
		}
	}
}

// TestEditor_ClientsAndWorkspaces verifies the client tier surfaces parents
// and Workspaces filters to a client's children with the ancestor chain.
func TestEditor_ClientsAndWorkspaces(t *testing.T) {
	ed, st, _, ctx := newEditor(t)
	_ = st.CreateWorkspace(ctx, &store.Workspace{ID: "acme", Name: "Acme"})
	_ = st.CreateWorkspace(ctx, &store.Workspace{ID: "acme-api", Name: "Acme API", ParentID: "acme"})

	clients, err := ed.Clients(ctx)
	if err != nil {
		t.Fatalf("Clients: %v", err)
	}
	var foundAcme bool
	for _, c := range clients {
		if c.ID == "acme" {
			foundAcme = true
			if c.WorkspaceCt != 1 {
				t.Fatalf("acme workspace_count = %d, want 1", c.WorkspaceCt)
			}
		}
	}
	if !foundAcme {
		t.Fatal("acme not surfaced as a client (parent) tier")
	}

	children, err := ed.Workspaces(ctx, "acme")
	if err != nil {
		t.Fatalf("Workspaces: %v", err)
	}
	if len(children) != 1 || children[0].ID != "acme-api" {
		t.Fatalf("Workspaces(acme) = %+v, want [acme-api]", children)
	}
	if len(children[0].Chain) != 2 || children[0].Chain[0] != "acme-api" || children[0].Chain[1] != "acme" {
		t.Fatalf("chain = %v, want [acme-api acme]", children[0].Chain)
	}
	if children[0].Source != store.IndexSourceCentral {
		t.Fatalf("source = %q, want central", children[0].Source)
	}
}

// TestEditor_Search_TiersAndCreateLabel runs the real FTS-backed search and
// asserts an exact-prefix hit outranks a fuzzy one, and the create-on-miss
// label echoes the typed text.
func TestEditor_Search_TiersAndCreateLabel(t *testing.T) {
	ed, _, _, ctx := newEditor(t)

	mk := func(title, status string) {
		if _, err := ed.SaveTask(ctx, brain.TaskRecord{Workspace: "ws", Title: title, Status: status}, nil); err != nil {
			t.Fatalf("SaveTask %q: %v", title, err)
		}
	}
	mk("Re-arm worker cron jobs", "open")  // exact-prefix on "re-arm"
	mk("Cap mesh history scope", "open")   // contains "arm"? no
	mk("Disarm the alarm handler", "open") // fuzzy substring "arm"

	res, err := ed.Search(ctx, "re-arm", "task", "ws", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected at least one hit for 're-arm'")
	}
	if res.Hits[0].Tier != 0 {
		t.Fatalf("top hit tier = %d, want 0 (exact-prefix)", res.Hits[0].Tier)
	}
	if res.CreateLabel != "re-arm" {
		t.Fatalf("create label = %q, want 're-arm'", res.CreateLabel)
	}

	// "arm" should reach the fuzzy tier for the disarm/re-arm titles.
	res2, err := ed.Search(ctx, "arm", "task", "ws", 10)
	if err != nil {
		t.Fatalf("Search arm: %v", err)
	}
	if len(res2.Hits) == 0 {
		t.Fatal("expected fuzzy/token hits for 'arm'")
	}
}

// TestEditor_IfHashConflict verifies a stale if_hash on a task PUT returns a
// ConflictDetail carrying the fresh on-disk record + a writer, before any
// mutation lands.
func TestEditor_IfHashConflict(t *testing.T) {
	ed, _, _, ctx := newEditor(t)

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{Workspace: "ws", Title: "Original", Status: "open"}, nil)
	if err != nil {
		t.Fatalf("SaveTask: %v", err)
	}

	// Simulate a concurrent out-of-band edit: append to the .md so its on-disk
	// hash diverges from what the editor loaded.
	raw, rerr := os.ReadFile(saved.Path)
	if rerr != nil {
		t.Fatalf("read .md: %v", rerr)
	}
	if werr := os.WriteFile(saved.Path, append(raw, []byte("\nedited out of band\n")...), 0o644); werr != nil {
		t.Fatalf("write .md: %v", werr)
	}

	// The editor submits the STALE if_hash it loaded with → conflict.
	_, err = ed.SaveTask(ctx, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "My edit", Status: "doing", IfHash: saved.OnDiskHash,
	}, nil)
	if err == nil {
		t.Fatal("expected a conflict on stale if_hash")
	}
	if !errors.Is(err, brain.ErrConflict) {
		t.Fatalf("conflict must unwrap to ErrConflict, got %v", err)
	}
	det, ok := brain.AsConflictDetail(err)
	if !ok {
		t.Fatalf("expected a ConflictDetail, got %v", err)
	}
	if det.OnDiskTask == nil || det.OnDiskTask.Title != "Original" {
		t.Fatalf("on-disk task should reflect the canonical .md (Original), got %+v", det.OnDiskTask)
	}
	if det.Writer == "" {
		t.Fatal("conflict detail must name a writer")
	}
	if det.OnDiskHash == "" || det.OnDiskHash == saved.OnDiskHash {
		t.Fatalf("on-disk hash must be the fresh divergent hash, got %q", det.OnDiskHash)
	}
}

// TestEditor_IfHashMatchSaves verifies a matching if_hash lets the save land.
func TestEditor_IfHashMatchSaves(t *testing.T) {
	ed, st, _, ctx := newEditor(t)
	saved, err := ed.SaveTask(ctx, brain.TaskRecord{Workspace: "ws", Title: "Original", Status: "open"}, nil)
	if err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	// Re-read the detail so we carry the live on_disk_hash.
	detail, err := ed.GetTaskDetail(ctx, saved.ID)
	if err != nil {
		t.Fatalf("GetTaskDetail: %v", err)
	}
	out, err := ed.SaveTask(ctx, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "Updated", Status: "doing", IfHash: detail.OnDiskHash,
	}, nil)
	if err != nil {
		t.Fatalf("SaveTask with matching if_hash: %v", err)
	}
	row, _ := st.GetTask(ctx, out.ID)
	if row.Title != "Updated" || row.Status != "doing" {
		t.Fatalf("expected the update to land, got %+v", row)
	}
}

// TestEditor_SuppressCandidate verifies the sticky suppression round-trips.
func TestEditor_SuppressCandidate(t *testing.T) {
	ed, st, _, ctx := newEditor(t)
	if err := ed.SuppressCandidate(ctx, "rec1", "hash-a"); err != nil {
		t.Fatalf("SuppressCandidate: %v", err)
	}
	yes, err := st.IsCandidateSuppressed(ctx, "rec1", "hash-a")
	if err != nil || !yes {
		t.Fatalf("expected hash-a suppressed, got %v/%v", yes, err)
	}
	no, _ := st.IsCandidateSuppressed(ctx, "rec1", "hash-b")
	if no {
		t.Fatal("hash-b must not be suppressed by the hash-a row")
	}
	// Suppress-all (blank hash) covers any candidate on the record.
	if err := ed.SuppressCandidate(ctx, "rec2", ""); err != nil {
		t.Fatalf("SuppressCandidate all: %v", err)
	}
	all, _ := st.IsCandidateSuppressed(ctx, "rec2", "anything")
	if !all {
		t.Fatal("blank-hash suppression must cover any candidate on the record")
	}
}

// TestEditor_TaskRecordEnriched verifies the browse projection carries the
// index source + on-disk hash + live-lease + source provenance.
func TestEditor_TaskRecordEnriched(t *testing.T) {
	ed, _, _, ctx := newEditor(t)
	saved, err := ed.SaveTask(ctx, brain.TaskRecord{Workspace: "ws", Title: "Enriched", Status: "open"}, nil)
	if err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	got, err := ed.GetTask(ctx, saved.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.OnDiskHash == "" {
		t.Fatal("expected an on_disk_hash CAS token")
	}
	if got.IndexSource != store.IndexSourceCentral {
		t.Fatalf("index_source = %q, want central", got.IndexSource)
	}
	if got.Source == nil || got.Source.Kind != "user" {
		t.Fatalf("expected source.kind=user, got %+v", got.Source)
	}
	if got.LiveLease {
		t.Fatal("a freshly-created task with no lease must not be live")
	}
}
