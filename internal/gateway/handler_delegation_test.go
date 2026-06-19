package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

func TestClientModelFromHint(t *testing.T) {
	tests := []struct {
		name string
		hint string
		want string
	}{
		{
			name: "name slash version gets prefix",
			hint: "claude-code/0.139.0",
			want: "client:claude-code/0.139.0",
		},
		{
			name: "version only gets prefix",
			hint: "0.139.0",
			want: "client:0.139.0",
		},
		{
			name: "empty hint returns empty",
			hint: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clientModelFromHint(tt.hint)
			if got != tt.want {
				t.Errorf("clientModelFromHint(%q) = %q, want %q", tt.hint, got, tt.want)
			}
		})
	}
}

func TestHandleListDelegationModelCapacityHonorsDisabledProviders(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/delegation-capacity.db")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "workers", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "test-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	for _, p := range []*store.ModelProfile{
		{
			Name:          "enabled-openai",
			Provider:      "openai",
			SecretScopeID: scope.ID,
			KnownModels:   []string{"gpt-reviewer"},
		},
		{
			Name:        "disabled-claude-cli",
			Provider:    "claude_cli",
			KnownModels: []string{"claude-opus-4-8"},
		},
	} {
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create model profile %s: %v", p.Name, err)
		}
	}

	settingsSvc := config.NewSettingsService(db)
	settings := config.DefaultSettings()
	settings.DelegationDisabledProviders = map[string]bool{"claude": true}
	if err := settingsSvc.Save(ctx, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	h := &handler{
		workerAdmin: workersadmin.New(db, workersadmin.Options{Workspaces: db}),
		settingsSvc: settingsSvc,
	}
	reqCtx := WithWorkerWorkspaceAccess(ctx, ws.ID, []WorkerWorkspaceGrant{
		{WorkspaceID: ws.ID, Access: store.WorkerWorkspaceAccessWrite, RootPath: "/repo"},
	})
	raw, rpcErr := h.handleListDelegationModelCapacity(reqCtx, json.RawMessage(`{"task_kind":"review","limit":20}`))
	if rpcErr != nil {
		t.Fatalf("handleListDelegationModelCapacity: %v", rpcErr)
	}

	var toolResult CallToolResult
	if err := json.Unmarshal(raw, &toolResult); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	if len(toolResult.Content) == 0 {
		t.Fatal("tool result content empty")
	}
	var payload struct {
		Capacity []workersadmin.DelegationModelCapacity `json:"capacity"`
	}
	if err := json.Unmarshal([]byte(toolResult.Content[0].Text), &payload); err != nil {
		t.Fatalf("unmarshal capacity payload: %v", err)
	}
	if len(payload.Capacity) == 0 {
		t.Fatal("expected at least one capacity row")
	}
	seenOpenAI := false
	for _, row := range payload.Capacity {
		if row.ModelProvider == "openai" && row.ModelID == "gpt-reviewer" {
			seenOpenAI = true
		}
		if row.ModelProvider == "claude_cli" || row.ModelKey == "claude_cli/claude-opus-4-8" {
			t.Fatalf("disabled claude capacity row leaked through MCP advice: %+v", row)
		}
	}
	if !seenOpenAI {
		t.Fatalf("enabled openai profile missing from capacity rows: %+v", payload.Capacity)
	}
}
