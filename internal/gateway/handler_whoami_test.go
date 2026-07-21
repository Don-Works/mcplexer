package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
)

func TestHandleWhoami(t *testing.T) {
	tests := []struct {
		name         string
		wsChain      []routing.WorkspaceAncestor
		clientPath   string
		wantID       string
		wantName     string
		wantBound    bool
		wantRoot     string
		wantChainLen int
	}{
		{
			name: "bound to a workspace",
			wsChain: []routing.WorkspaceAncestor{
				{ID: "ws-acme", Name: "Acme workspace", RootPath: "/workspace/acme"},
			},
			clientPath:   "/workspace/acme/mcplexer",
			wantID:       "ws-acme",
			wantName:     "Acme workspace",
			wantBound:    true,
			wantRoot:     "/workspace/acme/mcplexer",
			wantChainLen: 1,
		},
		{
			name:         "no workspace bound is reported, not hidden",
			wsChain:      nil,
			clientPath:   "/tmp",
			wantID:       "",
			wantName:     "",
			wantBound:    false,
			wantRoot:     "/tmp",
			wantChainLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &handler{sessions: &sessionManager{wsChain: tt.wsChain, clientPath: tt.clientPath}}

			raw, rpcErr := h.handleWhoami(context.Background())
			if rpcErr != nil {
				t.Fatalf("handleWhoami returned RPC error: %v", rpcErr)
			}

			// Unwrap the MCP envelope, then the inner JSON payload.
			var env CallToolResult
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.IsError {
				t.Fatalf("result marked isError: %s", raw)
			}
			if len(env.Content) == 0 {
				t.Fatal("empty content")
			}
			var got whoamiResult
			if err := json.Unmarshal([]byte(env.Content[0].Text), &got); err != nil {
				t.Fatalf("unmarshal whoamiResult: %v", err)
			}

			if got.WorkspaceID != tt.wantID {
				t.Errorf("WorkspaceID = %q, want %q", got.WorkspaceID, tt.wantID)
			}
			if got.WorkspaceName != tt.wantName {
				t.Errorf("WorkspaceName = %q, want %q", got.WorkspaceName, tt.wantName)
			}
			if got.WorkspaceBound != tt.wantBound {
				t.Errorf("WorkspaceBound = %v, want %v", got.WorkspaceBound, tt.wantBound)
			}
			if got.ClientRoot != tt.wantRoot {
				t.Errorf("ClientRoot = %q, want %q", got.ClientRoot, tt.wantRoot)
			}
			if len(got.WorkspaceChain) != tt.wantChainLen {
				t.Errorf("WorkspaceChain len = %d, want %d", len(got.WorkspaceChain), tt.wantChainLen)
			}
			if got.Summary == "" {
				t.Error("Summary must never be empty — it's the human-readable orientation line")
			}
		})
	}
}
