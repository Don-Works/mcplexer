package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// wireBrainMigration builds an InternalBackend with the M5 migration tooling
// wired against a fresh temp-dir brain repo + the given store.
func wireBrainMigration(t *testing.T, db *sqlite.DB) (*InternalBackend, brain.Config) {
	t.Helper()
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ix := brain.NewIndexer(cfg, db, nil)
	ser := brain.NewSerializer(cfg, db, nil)
	ser.ShareSelfWrites(ix)
	b := NewInternalBackend(db, nil)
	b.SetBrainMigration(cfg, ser, ix, db)
	return b, cfg
}

func TestCallBrainMigration_UnavailableErrors(t *testing.T) {
	b := NewInternalBackend(newTestDB(t), nil) // no migration wired
	ctx := context.Background()
	for _, tool := range []string{"brain_init", "brain_import", "brain_verify", "brain_disable"} {
		out, err := b.Call(ctx, tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("%s Call transport error: %v", tool, err)
		}
		text, isErr := callResult(t, out)
		if !isErr {
			t.Fatalf("%s with no brain wired should error, got %q", tool, text)
		}
		if !strings.Contains(text, "not available") {
			t.Fatalf("%s error = %q, want 'not available'", tool, text)
		}
	}
}

func TestBrainImport_ParityVerified(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{ID: "mw", Name: "MW"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := db.CreateTask(ctx, &store.Task{ID: "01MIGTASK001", WorkspaceID: "mw", Title: "T", Status: "open", Description: "B."}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	b, _ := wireBrainMigration(t, db)
	out, err := b.Call(ctx, "brain_import", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("brain_import Call: %v", err)
	}
	text, isErr := callResult(t, out)
	if isErr {
		t.Fatalf("brain_import unexpected error: %s", text)
	}
	var rep brain.ImportReport
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		t.Fatalf("decode ImportReport: %v\n%s", err, text)
	}
	if !rep.ParityOK {
		t.Fatalf("parity_ok false: drifts=%v errors=%v", rep.Drifts, rep.Errors)
	}
	if rep.Tasks != 1 || rep.Workspaces != 2 {
		t.Errorf("counts = tasks %d ws %d, want 1/2", rep.Tasks, rep.Workspaces)
	}
}

func TestBrainVerify_NoDrift(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "vw", Name: "VW"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := db.CreateTask(ctx, &store.Task{ID: "01VERTASK001", WorkspaceID: "vw", Title: "V", Status: "open"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	b, _ := wireBrainMigration(t, db)

	// Import first so files + index exist, then verify reports no drift.
	if _, err := b.Call(ctx, "brain_import", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("import: %v", err)
	}
	out, err := b.Call(ctx, "brain_verify", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("brain_verify Call: %v", err)
	}
	text, isErr := callResult(t, out)
	if isErr {
		t.Fatalf("brain_verify error: %s", text)
	}
	var res struct {
		OK     bool       `json:"ok"`
		Drifts []struct{} `json:"drifts"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode verify: %v\n%s", err, text)
	}
	if !res.OK {
		t.Errorf("verify ok=false, want true (drifts=%d)", len(res.Drifts))
	}
}

func TestBrainDisable_FlipsFlag(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Seed settings with brain_enabled=true (+ an unrelated key to confirm
	// it survives the merge).
	if err := db.UpdateSettings(ctx, json.RawMessage(`{"brain_enabled":true,"mesh_enabled":true}`)); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	b, _ := wireBrainMigration(t, db)
	out, err := b.Call(ctx, "brain_disable", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("brain_disable Call: %v", err)
	}
	if text, isErr := callResult(t, out); isErr {
		t.Fatalf("brain_disable error: %s", text)
	}

	raw, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if brain.SettingsEnabled(raw) {
		t.Errorf("brain_enabled still true after disable: %s", raw)
	}
	// Unrelated key preserved.
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["mesh_enabled"] != true {
		t.Errorf("mesh_enabled not preserved through disable merge: %s", raw)
	}
}
