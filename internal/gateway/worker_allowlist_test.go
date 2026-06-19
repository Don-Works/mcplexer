package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestCheckWorkerToolAllowlist(t *testing.T) {
	cases := []struct {
		name     string
		allow    []string
		toolName string
		wantErr  bool
	}{
		{"no worker context allows", nil, "github__list_issues", false},
		{"empty allowlist denies", []string{}, "github__list_issues", true},
		{"exact match allows", []string{"github__list_issues"}, "github__list_issues", false},
		{"exact miss denies", []string{"github__list_issues"}, "github__create_issue", true},
		{"glob match allows", []string{"github__*"}, "github__create_issue", false},
		{"glob miss denies", []string{"github__*"}, "linear__create_issue", true},
		{"blank pattern ignored", []string{""}, "github__list_issues", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.allow != nil {
				ctx = WithWorkerToolAllowlist(ctx, tc.allow)
			}
			err := checkWorkerToolAllowlist(ctx, tc.toolName)
			if tc.wantErr && err == nil {
				t.Fatalf("checkWorkerToolAllowlist(%q) = nil, want error", tc.toolName)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkWorkerToolAllowlist(%q) = %v, want nil", tc.toolName, err)
			}
		})
	}
}

func TestHandleDiscoverTools_WorkerAllowlistFiltersResults(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"github-srv": []byte(`{"tools":[
				{"name":"list_issues","description":"List GitHub issues"},
				{"name":"create_issue","description":"Create GitHub issues"}
			]}`),
			"linear-srv": []byte(`{"tools":[
				{"name":"create_issue","description":"Create Linear issues"}
			]}`),
		},
	}
	h, _ := newTestHandler(lister, []store.DownstreamServer{
		{ID: "github-srv", Name: "github", ToolNamespace: "github", Discovery: "static"},
		{ID: "linear-srv", Name: "linear", ToolNamespace: "linear", Discovery: "static"},
	})

	ctx := WithWorkerToolAllowlist(context.Background(), []string{"github__list_issues"})
	result, rpcErr := h.handleDiscoverTools(ctx, []string{"issues"}, "full", "", nil, 0)
	if rpcErr != nil {
		t.Fatalf("handleDiscoverTools: %v", rpcErr)
	}
	text := firstToolText(t, result)
	if !strings.Contains(text, "github__list_issues") {
		t.Fatalf("expected allowed tool in discovery output, got:\n%s", text)
	}
	if strings.Contains(text, "github__create_issue") || strings.Contains(text, "linear__create_issue") {
		t.Fatalf("disallowed tool leaked into discovery output:\n%s", text)
	}
}

func TestWorkerWorkspaceAccessReadWriteAndPreferredRouting(t *testing.T) {
	h := &handler{}
	ctx := WithWorkerWorkspaceAccess(context.Background(), "home", []WorkerWorkspaceGrant{
		{WorkspaceID: "readonly", Access: store.WorkerWorkspaceAccessRead, RootPath: "/repo/readonly"},
		{WorkspaceID: "home", Access: store.WorkerWorkspaceAccessWrite, RootPath: "/repo/home"},
	})

	if got := h.currentWorkspaceID(ctx); got != "home" {
		t.Fatalf("currentWorkspaceID = %q, want home", got)
	}
	ancestors := h.routingWorkspaceAncestors(ctx)
	if len(ancestors) != 2 || ancestors[0].ID != "home" {
		t.Fatalf("routing ancestors = %+v, want home first", ancestors)
	}
	if rpc := h.requireWorkerWorkspaceAccess(ctx, "readonly", false); rpc != nil {
		t.Fatalf("read access to readonly workspace denied: %v", rpc)
	}
	if rpc := h.requireWorkerWorkspaceAccess(ctx, "readonly", true); rpc == nil {
		t.Fatal("write access to readonly workspace allowed")
	}
	if rpc := h.requireWorkerWorkspaceAccess(ctx, "unknown", false); rpc == nil {
		t.Fatal("read access to ungranted workspace allowed")
	}
}

func firstToolText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var parsed CallToolResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if len(parsed.Content) == 0 {
		t.Fatal("tool result content empty")
	}
	return parsed.Content[0].Text
}
