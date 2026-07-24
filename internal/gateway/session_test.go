package gateway

import (
	"os"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
)

func TestIsPathAncestor(t *testing.T) {
	tests := []struct {
		name     string
		ancestor string
		path     string
		want     bool
	}{
		{"exact match", "/users/example/project", "/users/example/project", true},
		{"global root", "/", "/users/example/project", true},
		{"proper parent", "/users/example", "/users/example/project", true},
		{"deeper parent", "/users", "/users/example/project", true},
		{"partial name no boundary", "/users/m", "/users/example/project", false},
		{"no relation", "/opt/tools", "/users/example/project", false},
		{"child not ancestor", "/users/example/project/sub", "/users/example/project", false},
		{"sibling", "/users/example/other", "/users/example/project", false},
		{"root exact", "/", "/", true},
		{"trailing slash ancestor", "/users/example", "/users/example/", true},
		{"trailing slash ancestor in pattern", "/users/example/", "/users/example", true},
		{"trailing slash both", "/users/example/", "/users/example/", true},
		{"root ancestor of anything", "/", "/any", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPathAncestor(tt.ancestor, tt.path)
			if got != tt.want {
				t.Errorf("isPathAncestor(%q, %q) = %v, want %v",
					tt.ancestor, tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveWorkspaceChain_UsesProcessCWD(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() failed: %v", err)
	}

	sm := &sessionManager{
		store: &mockStore{
			workspaces: []mockWorkspace{
				{id: "ws-global", rootPath: "/"},
			},
		},
	}

	// In stdio mode, detectClientRoot uses os.Getwd() and ignores reported roots.
	sm.clientPath = sm.detectClientRoot([]Root{{URI: "file:///fake/spoofed/path"}})
	chain := sm.resolveChainForPath(t.Context(), sm.clientPath)

	// Should resolve based on actual CWD, not the spoofed root.
	// The "/" workspace is always an ancestor.
	if len(chain) == 0 {
		t.Fatal("expected at least the global workspace in chain")
	}
	if chain[0].ID != "ws-global" {
		t.Errorf("chain[0].ID = %q, want %q", chain[0].ID, "ws-global")
	}

	// Verify clientPath is set to actual CWD, not client-reported root.
	if sm.clientPath != cwd {
		t.Errorf("clientPath = %q, want CWD %q", sm.clientPath, cwd)
	}
}

func TestResolveWorkspaceChain_Ordering(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() failed: %v", err)
	}

	sm := &sessionManager{
		store: &mockStore{
			workspaces: []mockWorkspace{
				{id: "ws-global", rootPath: "/"},
				// Include a workspace that matches the actual CWD.
				{id: "ws-cwd", rootPath: cwd},
				{id: "ws-unrelated", rootPath: "/nonexistent/path/xyz"},
			},
		},
	}

	sm.clientPath = sm.detectClientRoot(nil)
	chain := sm.resolveChainForPath(t.Context(), sm.clientPath)

	// ws-cwd should be first (most specific), ws-global second, ws-unrelated excluded.
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want 2 (got %v)", len(chain), chain)
	}
	if chain[0].ID != "ws-cwd" {
		t.Errorf("chain[0].ID = %q, want %q", chain[0].ID, "ws-cwd")
	}
	if chain[1].ID != "ws-global" {
		t.Errorf("chain[1].ID = %q, want %q", chain[1].ID, "ws-global")
	}
}

func TestResolveWorkspaceChain_NoMatchingWorkspaces(t *testing.T) {
	sm := &sessionManager{
		store: &mockStore{
			workspaces: []mockWorkspace{
				{id: "ws-other", rootPath: "/nonexistent/specific/path"},
			},
		},
		clientPath: "/some/other/path",
	}

	chain := sm.resolveChainForPath(t.Context(), sm.clientPath)
	if len(chain) != 0 {
		t.Errorf("expected empty chain, got %v", chain)
	}
}

func TestValidateClientRoots_Consistent(t *testing.T) {
	sm := &sessionManager{}
	// No panic/error when root is consistent with CWD.
	sm.validateClientRoots("/home/user/project", []Root{
		{URI: "file:///home/user/project"},
	})
}

func TestValidateClientRoots_AncestorIsOK(t *testing.T) {
	sm := &sessionManager{}
	// Root that is an ancestor of CWD should be accepted.
	sm.validateClientRoots("/home/user/project/sub", []Root{
		{URI: "file:///home/user/project"},
	})
}

func TestValidateClientRoots_EmptyRoots(t *testing.T) {
	sm := &sessionManager{}
	// No roots → no validation needed.
	sm.validateClientRoots("/home/user", nil)
}

func TestCreateModelHint(t *testing.T) {
	tests := []struct {
		name string
		info ClientInfo
		want string
	}{
		{
			name: "name and version",
			info: ClientInfo{Name: "claude-code", Version: "0.139.0"},
			want: "claude-code/0.139.0",
		},
		{
			name: "empty name uses version only",
			info: ClientInfo{Name: "", Version: "0.139.0"},
			want: "0.139.0",
		},
		{
			name: "both empty",
			info: ClientInfo{Name: "", Version: ""},
			want: "",
		},
		{
			name: "empty version with name",
			info: ClientInfo{Name: "claude-code", Version: ""},
			want: "claude-code/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := &sessionManager{
				store:     &mockStore{},
				transport: TransportStdio,
			}
			if err := sm.create(t.Context(), tt.info, nil, "", nil); err != nil {
				t.Fatalf("create() error: %v", err)
			}
			got := sm.modelHint()
			if got != tt.want {
				t.Errorf("modelHint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkspaceID_EmptyChain(t *testing.T) {
	sm := &sessionManager{}
	if got := sm.workspaceID(); got != "" {
		t.Errorf("workspaceID() = %q, want empty", got)
	}
}

func TestWorkspaceID_ReturnsFirst(t *testing.T) {
	sm := &sessionManager{wsChain: []routing.WorkspaceAncestor{
		{ID: "ws-specific", RootPath: "/a"},
		{ID: "ws-global", RootPath: "/"},
	}}
	if got := sm.workspaceID(); got != "ws-specific" {
		t.Errorf("workspaceID() = %q, want %q", got, "ws-specific")
	}
}
