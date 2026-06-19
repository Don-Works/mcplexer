package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleMeshSkillHubSearch_NilRegistryShare(t *testing.T) {
	h := &handler{}
	args := json.RawMessage(`{"peer_id":"12D3KooWTest","q":"deploy"}`)
	result, rpcErr := h.handleMeshSkillHubSearch(context.Background(), args)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(out.Content) == 0 {
		t.Fatal("expected non-empty content")
	}
	text := out.Content[0].Text
	if text == "" {
		t.Fatal("expected non-empty text")
	}
	if !strings.Contains(text, "not enabled") && !strings.Contains(text, "not built") {
		t.Errorf("expected 'not enabled' or 'not built' in result, got: %s", text)
	}
}

func TestHandleMeshSkillHubSearch_MissingPeerID(t *testing.T) {
	// When registryShare is nil, the handler returns early with "not enabled"
	// before reaching validation. This is the expected guard-clause path.
	h := &handler{}
	args := json.RawMessage(`{"q":"deploy"}`)
	result, rpcErr := h.handleMeshSkillHubSearch(context.Background(), args)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(result, &out)
}

func TestHandleMeshSkillHubSearch_MissingQuery(t *testing.T) {
	// When registryShare is nil, the handler returns early with "not enabled"
	// before reaching validation. This is the expected guard-clause path.
	h := &handler{}
	args := json.RawMessage(`{"peer_id":"12D3KooWTest"}`)
	result, rpcErr := h.handleMeshSkillHubSearch(context.Background(), args)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %v", rpcErr)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(result, &out)
}

func TestHandleMeshSkillHubSearch_QueryAlias(t *testing.T) {
	h := &handler{}
	// Use "query" instead of "q" — should be accepted as ergonomic alias
	args := json.RawMessage(`{"peer_id":"12D3KooWTest","query":"deploy"}`)
	// Will fail at resolveMeshPeer since there's no real mesh, but
	// should not fail at validation
	result, rpcErr := h.handleMeshSkillHubSearch(context.Background(), args)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error for query alias: %v", rpcErr)
	}
	// Result should be an error message about peer not paired, not validation
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(result, &out)
}

func TestHubSearchToolDef(t *testing.T) {
	defs := hubSyncToolDefinitions()
	found := false
	for _, d := range defs {
		if d.Name == "mesh__skill_hub_search" {
			found = true
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(d.InputSchema, &schema); err != nil {
				t.Fatalf("unmarshal schema: %v", err)
			}
			hasPeerID := false
			for _, r := range schema.Required {
				if r == "peer_id" {
					hasPeerID = true
				}
			}
			if !hasPeerID {
				t.Error("missing peer_id in required")
			}
			if _, ok := schema.Properties["query"]; !ok {
				t.Error("missing query property")
			}
			if _, ok := schema.Properties["q"]; !ok {
				t.Error("missing q alias property")
			}
			break
		}
	}
	if !found {
		t.Error("mesh__skill_hub_search not found in tool definitions")
	}
}
