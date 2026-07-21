package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// seedRouteDeps creates the workspace + downstream server a route_rules row
// FK-references, so a CreateRouteRule under test does not fail on the FK.
func seedRouteDeps(t *testing.T, ctx context.Context, db store.Store) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "ws", Name: "WS", RootPath: "/ws", DefaultPolicy: "deny",
		Source: "api", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "srv", Name: "srv", Transport: "stdio", ToolNamespace: "srv",
		Args: json.RawMessage("[]"), Source: "api", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed downstream: %v", err)
	}
}

// writeRoutesYAML writes a workspaces/<ws>/config/routes.yaml under brainDir.
func writeRoutesYAML(t *testing.T, brainDir, ws, body string) {
	t.Helper()
	dir := filepath.Join(brainDir, "workspaces", ws, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routes.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write routes.yaml: %v", err)
	}
}

func TestApplyBrain_UpsertAndPrune(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedRouteDeps(t, ctx, db)

	// A pre-existing source="yaml" rule must survive the brain apply.
	now := time.Now().UTC()
	if err := db.CreateRouteRule(ctx, &store.RouteRule{
		ID: "yaml-rule", WorkspaceID: "ws", PathGlob: "**",
		ToolMatch: json.RawMessage(`["*"]`), DownstreamServerID: "srv",
		Policy: "allow", Source: "yaml", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed yaml rule: %v", err)
	}

	brainDir := t.TempDir()
	writeRoutesYAML(t, brainDir, "ws", `
routes:
  - id: brain-r1
    name: Brain rule one
    priority: 50
    tool_match: ["github__*"]
    downstream_server_id: srv
    policy: allow
  - id: brain-r2
    priority: 40
    downstream_server_id: srv
    policy: deny
`)

	if err := ApplyBrain(ctx, db, brainDir); err != nil {
		t.Fatalf("ApplyBrain: %v", err)
	}

	rules, err := db.ListRouteRules(ctx, "")
	if err != nil {
		t.Fatalf("ListRouteRules: %v", err)
	}
	byID := map[string]store.RouteRule{}
	for _, r := range rules {
		byID[r.ID] = r
	}
	if r, ok := byID["brain-r1"]; !ok {
		t.Error("brain-r1 not created")
	} else if r.Source != "brain" || r.Priority != 50 {
		t.Errorf("brain-r1 wrong: %+v", r)
	}
	if _, ok := byID["brain-r2"]; !ok {
		t.Error("brain-r2 not created")
	}
	if _, ok := byID["yaml-rule"]; !ok {
		t.Error("source=yaml rule was clobbered by the brain apply")
	}

	// Drop brain-r2 from the file → a re-apply prunes it, keeps brain-r1
	// and the yaml rule.
	writeRoutesYAML(t, brainDir, "ws", `
routes:
  - id: brain-r1
    name: Brain rule one
    priority: 50
    tool_match: ["github__*"]
    downstream_server_id: srv
    policy: allow
`)
	if err := ApplyBrain(ctx, db, brainDir); err != nil {
		t.Fatalf("re-ApplyBrain: %v", err)
	}
	rules, _ = db.ListRouteRules(ctx, "")
	byID = map[string]store.RouteRule{}
	for _, r := range rules {
		byID[r.ID] = r
	}
	if _, ok := byID["brain-r2"]; ok {
		t.Error("brain-r2 should have been pruned")
	}
	if _, ok := byID["brain-r1"]; !ok {
		t.Error("brain-r1 should still exist")
	}
	if _, ok := byID["yaml-rule"]; !ok {
		t.Error("yaml rule should still exist after prune")
	}
}

// TestApplyBrain_Validation covers the regression where brain routes bypassed
// the API path's validation: a policy typo silently became an ALLOW rule, and
// a route referencing a non-existent workspace/downstream inserted a dangling
// row (migration 004 dropped the FKs). Every case must reject the apply AND
// leave the route table untouched (atomic — the apply runs in one tx).
func TestApplyBrain_Validation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string // substring expected in the error
	}{
		{
			name: "policy typo rejected (would silently ALLOW)",
			body: `
routes:
  - id: bad-policy
    priority: 50
    downstream_server_id: srv
    policy: dney
`,
			wantErr: "invalid policy",
		},
		{
			name: "missing id rejected",
			body: `
routes:
  - priority: 50
    downstream_server_id: srv
    policy: deny
`,
			wantErr: "missing id",
		},
		{
			name: "invalid glob rejected",
			body: `
routes:
  - id: bad-glob
    priority: 50
    path_glob: "[unterminated"
    downstream_server_id: srv
    policy: allow
`,
			wantErr: "invalid glob",
		},
		{
			name: "non-existent downstream rejected (dangling FK)",
			body: `
routes:
  - id: dangling-srv
    priority: 50
    downstream_server_id: does-not-exist
    policy: allow
`,
			wantErr: "downstream",
		},
		{
			name: "non-existent auth scope rejected",
			body: `
routes:
  - id: dangling-scope
    priority: 50
    downstream_server_id: srv
    auth_scope_id: does-not-exist
    policy: allow
`,
			wantErr: "auth scope",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			seedRouteDeps(t, ctx, db)

			brainDir := t.TempDir()
			writeRoutesYAML(t, brainDir, "ws", tc.body)

			err = ApplyBrain(ctx, db, brainDir)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}

			// The bad apply must not have inserted any brain row (atomic tx).
			rules, lErr := db.ListRouteRules(ctx, "")
			if lErr != nil {
				t.Fatalf("ListRouteRules: %v", lErr)
			}
			for _, r := range rules {
				if r.Source == "brain" {
					t.Errorf("brain row %q persisted despite failed apply", r.ID)
				}
			}
		})
	}
}

// TestApplyBrain_NonExistentWorkspace verifies a route under a workspace
// folder that has no matching workspace row is rejected (the workspace ref is
// taken from the folder name). Kept separate because it seeds no workspace.
func TestApplyBrain_NonExistentWorkspace(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Deliberately do NOT seed the "ghost-ws" workspace.
	now := time.Now().UTC()
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "srv", Name: "srv", Transport: "stdio", ToolNamespace: "srv",
		Args: json.RawMessage("[]"), Source: "api", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed downstream: %v", err)
	}

	brainDir := t.TempDir()
	writeRoutesYAML(t, brainDir, "ghost-ws", `
routes:
  - id: ghost-route
    priority: 50
    downstream_server_id: srv
    policy: allow
`)
	err = ApplyBrain(ctx, db, brainDir)
	if err == nil {
		t.Fatal("expected error for route under non-existent workspace, got nil")
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("error %q does not mention workspace", err.Error())
	}
}

func TestApplyBrain_NoBrainDir(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// A non-existent brain dir is a clean no-op (just prunes nothing).
	if err := ApplyBrain(ctx, db, filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("ApplyBrain on absent dir: %v", err)
	}
}
