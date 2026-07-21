package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdminCWDGate_IsAdminCWD(t *testing.T) {
	dataDir := filepath.Clean("/Users/test/.mcplexer")
	g := NewAdminCWDGate(dataDir)

	cases := []struct {
		cwd  string
		want bool
	}{
		{dataDir, true},
		{dataDir + "/skills", true},
		{dataDir + "/sub/dir", true},
		{"/Users/test", false},
		{"/Users/test/code/project", false},
		{"/", false},
		{"", false},
		{"/Users/other/.mcplexer", false}, // not the configured dir
		{dataDir + "fake", false},         // prefix-but-not-segment match
	}
	for _, c := range cases {
		if got := g.IsAdminCWD(c.cwd); got != c.want {
			t.Errorf("IsAdminCWD(%q) = %v, want %v", c.cwd, got, c.want)
		}
	}
}

func TestAdminCWDGate_DisabledWhenEmpty(t *testing.T) {
	g := NewAdminCWDGate("")
	if g.Enabled() {
		t.Error("empty dataDir should disable the gate")
	}
	if !g.IsAdminCWD("/anywhere") {
		t.Error("disabled gate should let everything through")
	}
}

func TestIsAdminTool(t *testing.T) {
	cases := []struct {
		name  string
		admin bool
	}{
		// Universal mcpx tools — never admin.
		{BuiltinPrefix + "search_tools", false},
		{BuiltinPrefix + "execute_code", false},

		// Admin mcpx tools.
		{BuiltinPrefix + "provision_mcp", true},
		{BuiltinPrefix + "create_addon", true},
		{BuiltinPrefix + "import_openapi", true},
		{BuiltinPrefix + "approve_tool_call", true},
		{BuiltinPrefix + "deny_tool_call", true},
		{BuiltinPrefix + "list_pending_approvals", true},
		{BuiltinPrefix + "reload_server", true},
		{BuiltinPrefix + "flush_cache", true},
		{BuiltinPrefix + "skill_install", true},

		// Legacy `mcplexer__` prefix maps onto the same admin set.
		{"mcplexer__provision_mcp", true},
		{"mcplexer__search_tools", true}, // every mcplexer__* counts as admin
		{"mcplexer__list_workspaces", true},

		// M0.5 worker admin tools — gated by the mcplexer__ prefix
		// rule, no special-casing required.
		{"mcplexer__list_workers", true},
		{"mcplexer__get_worker", true},
		{"mcplexer__create_worker", true},
		{"mcplexer__update_worker", true},
		{"mcplexer__delete_worker", true},
		{"mcplexer__pause_worker", true},
		{"mcplexer__resume_worker", true},
		{"mcplexer__run_worker_now", true},
		{"mcplexer__list_worker_runs", true},
		{"mcplexer__get_worker_run", true},

		// Mesh + secret + chat are universal.
		{"mesh__send", false},
		{"mesh__receive", false},
		{"secret__prompt", false},
		{"chat__send_message", false},

		// Random downstream tools — universal.
		{"github__create_issue", false},
		{"linear__list_issues", false},

		{"", false},
	}
	for _, c := range cases {
		if got := IsAdminTool(c.name); got != c.admin {
			t.Errorf("IsAdminTool(%q) = %v, want %v", c.name, got, c.admin)
		}
	}
}

func TestAdminCWDGate_DevModeSourceRepo(t *testing.T) {
	// A directory containing a go.mod that declares the mcplexer module
	// should be treated as admin context, even though it lives outside
	// the configured data dir. Mirrors the layer-2 hook's escape hatch.
	dataDir := filepath.Clean("/Users/test/.mcplexer")
	g := NewAdminCWDGate(dataDir)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"),
		[]byte("module github.com/don-works/mcplexer\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}

	t.Run("repo root is admin", func(t *testing.T) {
		if !g.IsAdminCWD(repo) {
			t.Errorf("expected admin lift at repo root %q", repo)
		}
	})

	t.Run("subdirectory is admin", func(t *testing.T) {
		sub := filepath.Join(repo, "internal", "gateway")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if !g.IsAdminCWD(sub) {
			t.Errorf("expected admin lift in %q", sub)
		}
	})

	t.Run("sibling directory with no go.mod is not admin", func(t *testing.T) {
		sibling := t.TempDir() // distinct tempdir, no go.mod
		if g.IsAdminCWD(sibling) {
			t.Errorf("expected non-admin in %q (no go.mod)", sibling)
		}
	})

	t.Run("directory with wrong-module go.mod is not admin", func(t *testing.T) {
		other := t.TempDir()
		if err := os.WriteFile(filepath.Join(other, "go.mod"),
			[]byte("module github.com/other/repo\n"), 0o644); err != nil {
			t.Fatalf("seed go.mod: %v", err)
		}
		if g.IsAdminCWD(other) {
			t.Errorf("expected non-admin in %q (different module)", other)
		}
	})
}

// TestAdminCWDGate_IsAdminContext_Socket covers the daemon/socket path,
// where the client doesn't advertise the source repo as an MCP root so
// clientRoot() (the cwd arg) is empty. The gate must still lift when a
// registered workspace root points at a mcplexer source tree, and must
// stay shut when the workspace root is an ordinary project directory.
func TestAdminCWDGate_IsAdminContext_Socket(t *testing.T) {
	dataDir := filepath.Clean("/Users/test/.mcplexer")
	g := NewAdminCWDGate(dataDir)

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"),
		[]byte("module github.com/don-works/mcplexer\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
	nonModule := t.TempDir() // ordinary project dir, no go.mod

	t.Run("empty cwd + module workspace root lifts the gate", func(t *testing.T) {
		if !g.IsAdminContext("", []string{repo}) {
			t.Errorf("expected admin lift for workspace root %q over socket", repo)
		}
	})

	t.Run("empty cwd + module workspace subdir lifts the gate", func(t *testing.T) {
		sub := filepath.Join(repo, "internal", "gateway")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if !g.IsAdminContext("", []string{sub}) {
			t.Errorf("expected admin lift for workspace subdir %q", sub)
		}
	})

	t.Run("empty cwd + non-module workspace root keeps gate shut", func(t *testing.T) {
		if g.IsAdminContext("", []string{nonModule}) {
			t.Errorf("expected non-admin for non-module workspace root %q", nonModule)
		}
	})

	t.Run("empty cwd + no workspace roots keeps gate shut", func(t *testing.T) {
		if g.IsAdminContext("", nil) {
			t.Error("expected non-admin with empty cwd and no workspace roots")
		}
	})

	t.Run("one module root among several lifts the gate", func(t *testing.T) {
		if !g.IsAdminContext("", []string{nonModule, repo}) {
			t.Error("expected admin lift when any workspace root is a module tree")
		}
	})

	t.Run("data-dir cwd still lifts regardless of workspace roots", func(t *testing.T) {
		if !g.IsAdminContext(dataDir+"/sub", []string{nonModule}) {
			t.Error("expected admin lift for data-dir cwd")
		}
	})

	t.Run("disabled gate passes through", func(t *testing.T) {
		open := NewAdminCWDGate("")
		if !open.IsAdminContext("", nil) {
			t.Error("disabled gate should pass everything through")
		}
	})
}

func TestFilterAdminTools(t *testing.T) {
	tools := []Tool{
		{Name: BuiltinPrefix + "search_tools"},
		{Name: BuiltinPrefix + "execute_code"},
		{Name: BuiltinPrefix + "provision_mcp"},
		{Name: BuiltinPrefix + "approve_tool_call"},
		{Name: BuiltinPrefix + "skill_install"},
		{Name: "mcplexer__list_workspaces"},
		{Name: "mcplexer__delete_workspace"},
		// M0.5 worker admin surface — gated identically to the legacy
		// mcplexer__* surface.
		{Name: "mcplexer__list_workers"},
		{Name: "mcplexer__create_worker"},
		{Name: "mcplexer__run_worker_now"},
		{Name: "mesh__send"},
		{Name: "github__create_issue"},
	}

	g := NewAdminCWDGate("/data")

	t.Run("admin CWD sees everything", func(t *testing.T) {
		out := g.FilterAdminTools(tools, "/data/some/sub", nil)
		if len(out) != len(tools) {
			t.Errorf("admin CWD: got %d tools, want %d", len(out), len(tools))
		}
	})

	t.Run("non-admin CWD strips admin tools", func(t *testing.T) {
		out := g.FilterAdminTools(tools, "/Users/me/project", nil)
		got := make(map[string]bool)
		for _, tool := range out {
			got[tool.Name] = true
		}
		// Universal tools must remain.
		for _, want := range []string{
			BuiltinPrefix + "search_tools",
			BuiltinPrefix + "execute_code",
			"mesh__send",
			"github__create_issue",
		} {
			if !got[want] {
				t.Errorf("non-admin CWD: missing universal tool %q", want)
			}
		}
		// Admin tools must be gone.
		for _, dropped := range []string{
			BuiltinPrefix + "provision_mcp",
			BuiltinPrefix + "approve_tool_call",
			BuiltinPrefix + "skill_install",
			"mcplexer__list_workspaces",
			"mcplexer__delete_workspace",
			"mcplexer__list_workers",
			"mcplexer__create_worker",
			"mcplexer__run_worker_now",
		} {
			if got[dropped] {
				t.Errorf("non-admin CWD: admin tool %q should be hidden", dropped)
			}
		}
	})
}
