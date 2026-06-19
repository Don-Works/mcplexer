package routing

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// tm is a shorthand for creating json.RawMessage tool match arrays.
func tm(patterns ...string) json.RawMessage {
	b, _ := json.Marshal(patterns)
	return b
}

// rrule builds a minimal store.RouteRule for integration tests.
func rrule(id, wsID, pathGlob, policy string, priority int, toolMatch json.RawMessage, dsID string) store.RouteRule {
	return store.RouteRule{
		ID: id, WorkspaceID: wsID, PathGlob: pathGlob,
		Policy: policy, Priority: priority, ToolMatch: toolMatch,
		DownstreamServerID: dsID,
	}
}

// approvalRule builds a RouteRule with approval fields set.
func approvalRule(id, wsID, pathGlob string, priority int, toolMatch json.RawMessage, dsID string, timeout int) store.RouteRule {
	r := rrule(id, wsID, pathGlob, "allow", priority, toolMatch, dsID)
	r.ApprovalMode = "all"
	r.ApprovalTimeout = timeout
	return r
}

// assertRoute checks both error type and matched rule ID.
func assertRoute(t *testing.T, result *RouteResult, err error, wantID string, wantErr error) {
	t.Helper()
	if wantErr != nil {
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if errors.Is(wantErr, ErrDenied) && wantID != "" {
			var de *DeniedError
			if errors.As(err, &de) && de.RuleID != wantID {
				t.Errorf("denied by %q, want %q", de.RuleID, wantID)
			}
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.MatchedRuleID != wantID {
		t.Errorf("matched rule = %q, want %q", result.MatchedRuleID, wantID)
	}
}

func TestIntegration_DirectoryHierarchy(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws1": {
				rrule("global-deny", "ws1", "**", "deny", 0, tm("*"), ""),
				rrule("root-allow", "ws1", "**", "allow", 10, tm("slack__*"), "slack-srv"),
				rrule("src-allow", "ws1", "src/**", "allow", 10, tm("github__*"), "gh-srv"),
				rrule("src-deny-tests", "ws1", "src/test/**", "deny", 10, tm("github__*"), ""),
				rrule("docs-allow", "ws1", "docs/**", "allow", 10, tm("notion__*"), "notion-srv"),
			},
		},
	})

	tests := []struct {
		name, subpath, tool, wantID string
		wantErr                     error
	}{
		{"slack at root", "", "slack__post", "root-allow", nil},
		{"slack in deep subdir", "src/pkg/deep", "slack__post", "root-allow", nil},
		{"github in src", "src/api", "github__pr", "src-allow", nil},
		{"github in src nested", "src/pkg/internal/handler", "github__pr", "src-allow", nil},
		{"github at root", "", "github__pr", "global-deny", ErrDenied},
		{"github in docs", "docs/guide", "github__pr", "global-deny", ErrDenied},
		{"github in src/test excluded", "src/test/unit", "github__pr", "src-deny-tests", ErrDenied},
		{"notion in docs", "docs/api-ref", "notion__page", "docs-allow", nil},
		{"notion at root", "", "notion__page", "global-deny", ErrDenied},
		{"unknown tool anywhere", "src/api", "unknown__tool", "global-deny", ErrDenied},
		{"sibling isolation", "docs", "github__pr", "global-deny", ErrDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Route(t.Context(), RouteContext{
				WorkspaceID: "ws1", Subpath: tt.subpath, ToolName: tt.tool,
			})
			assertRoute(t, result, err, tt.wantID, tt.wantErr)
		})
	}
}

func TestIntegration_ParentChildDenyAllow(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws1": {
				rrule("parent-deny", "ws1", "work/**", "deny", 100, tm("db__*"), ""),
				rrule("child-allow", "ws1", "work/staging/**", "allow", 1, tm("db__*"), "db-srv"),
				rrule("grandchild-deny", "ws1", "work/staging/migrations/**", "deny", 1, tm("db__*"), ""),
				rrule("catch-all", "ws1", "**", "allow", 0, tm("db__*"), "db-srv"),
			},
		},
	})

	tests := []struct {
		name, subpath, tool, wantID string
		wantErr                     error
	}{
		{"parent deny", "work/prod", "db__query", "parent-deny", ErrDenied},
		{"child allow overrides", "work/staging/app", "db__query", "child-allow", nil},
		{"grandchild deny", "work/staging/migrations/v1", "db__query", "grandchild-deny", ErrDenied},
		{"outside work catch-all", "other/path", "db__query", "catch-all", nil},
		{"work root parent deny", "work/anything", "db__query", "parent-deny", ErrDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Route(t.Context(), RouteContext{
				WorkspaceID: "ws1", Subpath: tt.subpath, ToolName: tt.tool,
			})
			assertRoute(t, result, err, tt.wantID, tt.wantErr)
		})
	}
}

func TestIntegration_WorkspaceFallbackWithPaths(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws-project": {
				rrule("proj-src-gh", "ws-project", "src/**", "allow", 0, tm("github__*"), "gh-srv"),
				rrule("proj-deny-vendor", "ws-project", "vendor/**", "deny", 0, tm("*"), ""),
			},
			"ws-org": {
				rrule("org-gh", "ws-org", "**", "allow", 0, tm("github__*"), "gh-srv"),
				rrule("org-slack", "ws-org", "**", "allow", 0, tm("slack__*"), "slack-srv"),
			},
			"ws-global": {
				rrule("global-pg", "ws-global", "**", "allow", 0, tm("postgres__*"), "pg-srv"),
			},
		},
	})

	ancestors := []WorkspaceAncestor{
		{ID: "ws-project", RootPath: "/home/user/project"},
		{ID: "ws-org", RootPath: "/home/user"},
		{ID: "ws-global", RootPath: "/"},
	}

	tests := []struct {
		name, clientRoot, tool, wantID string
		wantErr                        error
	}{
		{"src match at project", "/home/user/project/src/api", "github__pr", "proj-src-gh", nil},
		{"docs fallback to org", "/home/user/project/docs", "github__pr", "org-gh", nil},
		{"root fallback to org", "/home/user/project", "github__pr", "org-gh", nil},
		{"slack fallback to org", "/home/user/project/src", "slack__post", "org-slack", nil},
		{"postgres fallback to global", "/home/user/project/src", "postgres__query", "global-pg", nil},
		{"vendor deny stops chain", "/home/user/project/vendor/lib", "github__pr", "proj-deny-vendor", ErrDenied},
		{"unknown tool no route", "/home/user/project/src", "unknown__x", "", ErrNoRoute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.RouteWithFallback(t.Context(), RouteContext{
				ToolName: tt.tool,
			}, tt.clientRoot, ancestors)
			assertRoute(t, result, err, tt.wantID, tt.wantErr)
		})
	}
}

func TestIntegration_MonorepoRouting(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws-mono": {
				rrule("svc-api", "ws-mono", "services/api/**", "allow", 0, tm("*"), "api-server"),
				rrule("svc-web", "ws-mono", "services/web/**", "allow", 0, tm("*"), "web-server"),
				rrule("shared-libs", "ws-mono", "libs/**", "allow", 0, tm("github__*"), "gh-server"),
				rrule("infra-deny", "ws-mono", "infra/**", "deny", 0, tm("deploy__*"), ""),
				rrule("infra-allow-read", "ws-mono", "infra/**", "allow", 0, tm("terraform__plan"), "tf-server"),
				rrule("global-deny", "ws-mono", "**", "deny", 0, tm("*"), ""),
			},
		},
	})

	tests := []struct {
		name, subpath, tool string
		wantDS              string
		wantErr             error
	}{
		{"api tool in api dir", "services/api/handlers", "linear__task", "api-server", nil},
		{"web tool in web dir", "services/web/components", "figma__export", "web-server", nil},
		{"api tool in web dir", "services/web/components", "linear__task", "web-server", nil},
		{"github in libs", "libs/common/util", "github__pr", "gh-server", nil},
		{"deploy in infra denied", "infra/k8s", "deploy__apply", "", ErrDenied},
		{"terraform plan in infra", "infra/terraform", "terraform__plan", "tf-server", nil},
		{"anything at root denied", "", "linear__task", "", ErrDenied},
		{"cross-service wildcard", "services/api", "figma__export", "api-server", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Route(t.Context(), RouteContext{
				WorkspaceID: "ws-mono", Subpath: tt.subpath, ToolName: tt.tool,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q", result.DownstreamServerID, tt.wantDS)
			}
		})
	}
}

func TestIntegration_NamespaceWithPaths(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws1": {
				rrule("frontend-tools", "ws1", "web/**", "allow", 0, tm("*"), "figma-server"),
				rrule("backend-tools", "ws1", "api/**", "allow", 0, tm("*"), "linear-server"),
				rrule("global-deny", "ws1", "**", "deny", 0, tm("*"), ""),
			},
		},
		downstreams: map[string]*store.DownstreamServer{
			"figma-server":  {ID: "figma-server", ToolNamespace: "figma"},
			"linear-server": {ID: "linear-server", ToolNamespace: "linear"},
		},
	})

	tests := []struct {
		name, subpath, tool string
		wantDS              string
		wantErr             error
	}{
		{"figma in web", "web/components", "figma__export", "figma-server", nil},
		{"linear in web wrong ns", "web/components", "linear__task", "", ErrDenied},
		{"linear in api", "api/handlers", "linear__task", "linear-server", nil},
		{"figma in api wrong ns", "api/handlers", "figma__export", "", ErrDenied},
		{"unknown tool", "web/components", "slack__post", "", ErrDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Route(t.Context(), RouteContext{
				WorkspaceID: "ws1", Subpath: tt.subpath, ToolName: tt.tool,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q", result.DownstreamServerID, tt.wantDS)
			}
		})
	}
}

func TestIntegration_ApprovalPropagation(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws1": {
				// Broad rule: all deploy tools require approval with 60s timeout.
				approvalRule("deploy-approval", "ws1", "**", 0, tm("deploy__*"), "deploy-srv", 60),
				// Specific override: staging deploys are pre-approved (no approval).
				rrule("staging-deploy", "ws1", "staging/**", "allow", 0, tm("deploy__*"), "deploy-srv"),
				// Read tools allowed globally without approval.
				rrule("read-allow", "ws1", "**", "allow", 0, tm("read__*"), "read-srv"),
				// But read tools in secrets/ require approval.
				approvalRule("read-secrets", "ws1", "secrets/**", 0, tm("read__*"), "read-srv", 30),
				// Infra tools require approval with a longer timeout.
				approvalRule("infra-approval", "ws1", "infra/**", 0, tm("*"), "infra-srv", 300),
				// Catch-all deny.
				rrule("global-deny", "ws1", "**", "deny", 0, tm("*"), ""),
			},
		},
	})

	tests := []struct {
		name             string
		subpath, tool    string
		wantApprovalMode string
		wantTimeout      int
		wantID           string
		wantErr          error
	}{
		{
			name:    "deploy at root requires approval",
			subpath: "prod", tool: "deploy__apply",
			wantApprovalMode: "all", wantTimeout: 60, wantID: "deploy-approval",
		},
		{
			name:    "staging deploy no approval (specific override)",
			subpath: "staging/app", tool: "deploy__apply",
			wantApprovalMode: "", wantTimeout: 0, wantID: "staging-deploy",
		},
		{
			name:    "read tool without approval by default",
			subpath: "prod", tool: "read__status",
			wantApprovalMode: "", wantTimeout: 0, wantID: "read-allow",
		},
		{
			name:    "read tool in secrets requires approval",
			subpath: "secrets/vault", tool: "read__status",
			wantApprovalMode: "all", wantTimeout: 30, wantID: "read-secrets",
		},
		{
			name:    "infra tool requires approval with long timeout",
			subpath: "infra/k8s", tool: "terraform__apply",
			wantApprovalMode: "all", wantTimeout: 300, wantID: "infra-approval",
		},
		{
			name:    "infra deploy uses infra rule (more specific path)",
			subpath: "infra/cd", tool: "deploy__apply",
			wantApprovalMode: "all", wantTimeout: 300, wantID: "infra-approval",
		},
		{
			name:    "denied tool has no approval fields",
			subpath: "prod", tool: "unknown__x",
			wantErr: ErrDenied, wantID: "global-deny",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Route(t.Context(), RouteContext{
				WorkspaceID: "ws1", Subpath: tt.subpath, ToolName: tt.tool,
			})
			assertRoute(t, result, err, tt.wantID, tt.wantErr)
			if tt.wantErr != nil {
				return
			}
			if result.ApprovalMode != tt.wantApprovalMode {
				t.Errorf("ApprovalMode = %q, want %q", result.ApprovalMode, tt.wantApprovalMode)
			}
			if result.ApprovalTimeout != tt.wantTimeout {
				t.Errorf("ApprovalTimeout = %d, want %d", result.ApprovalTimeout, tt.wantTimeout)
			}
		})
	}
}

func TestIntegration_ApprovalWithFallback(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws-project": {
				// Project-level: github tools require approval.
				approvalRule("proj-gh-approval", "ws-project", "**", 0, tm("github__*"), "gh-srv", 120),
			},
			"ws-org": {
				// Org-level: github tools allowed without approval.
				rrule("org-gh", "ws-org", "**", "allow", 0, tm("github__*"), "gh-srv"),
				// Org-level: slack tools require approval.
				approvalRule("org-slack-approval", "ws-org", "**", 0, tm("slack__*"), "slack-srv", 30),
			},
		},
	})

	ancestors := []WorkspaceAncestor{
		{ID: "ws-project", RootPath: "/home/user/project"},
		{ID: "ws-org", RootPath: "/home/user"},
	}

	tests := []struct {
		name             string
		clientRoot       string
		tool             string
		wantApprovalMode string
		wantTimeout      int
		wantID           string
	}{
		{
			name:       "github matches project with approval",
			clientRoot: "/home/user/project/src", tool: "github__pr",
			wantApprovalMode: "all", wantTimeout: 120, wantID: "proj-gh-approval",
		},
		{
			name:       "slack falls through to org with approval",
			clientRoot: "/home/user/project/src", tool: "slack__post",
			wantApprovalMode: "all", wantTimeout: 30, wantID: "org-slack-approval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.RouteWithFallback(t.Context(), RouteContext{
				ToolName: tt.tool,
			}, tt.clientRoot, ancestors)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if result.MatchedRuleID != tt.wantID {
				t.Errorf("matched rule = %q, want %q", result.MatchedRuleID, tt.wantID)
			}
			if result.ApprovalMode != tt.wantApprovalMode {
				t.Errorf("ApprovalMode = %q, want %q", result.ApprovalMode, tt.wantApprovalMode)
			}
			if result.ApprovalTimeout != tt.wantTimeout {
				t.Errorf("ApprovalTimeout = %d, want %d", result.ApprovalTimeout, tt.wantTimeout)
			}
		})
	}
}

func TestIntegration_ComputeSubpath_EdgeCases(t *testing.T) {
	engine := NewEngine(&mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws1": {
				rrule("src-allow", "ws1", "src/**", "allow", 0, tm("*"), "srv"),
				rrule("catch-deny", "ws1", "**", "deny", 0, tm("*"), ""),
			},
		},
	})

	tests := []struct {
		name, clientRoot, wsRoot string
		wantErr                  error
	}{
		{"trailing slash client", "/home/project/src/", "/home/project", nil},
		{"trailing slash workspace", "/home/project/src", "/home/project/", nil},
		{"both trailing slashes", "/home/project/src/", "/home/project/", nil},
		{"prefix not child", "/home/user/proj-old", "/home/user/proj", ErrDenied},
		{"deep nesting 10+ levels", "/a/b/c/d/e/f/g/h/i/j/src/k", "/a/b/c/d/e/f/g/h/i/j", nil},
		{"root workspace match", "/src/api", "/", nil},
		{"root workspace no match", "/home/user/api", "/", ErrDenied},
		{"unicode segments", "/home/user/проект/src/api", "/home/user/проект", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ancestors := []WorkspaceAncestor{{ID: "ws1", RootPath: tt.wsRoot}}
			_, err := engine.RouteWithFallback(t.Context(), RouteContext{
				ToolName: "x__y",
			}, tt.clientRoot, ancestors)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
