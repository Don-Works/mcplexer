package hammerspoon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestExecLuaGate_OffFilters verifies ListTools omits exec_lua when the gate
// is off, even when a bridge is wired.
func TestExecLuaGate_OffFilters(t *testing.T) {
	s := NewMCPServer(NewManager(nullBridge{}, false))
	raw, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if strings.Contains(string(raw), `"name":"exec_lua"`) {
		t.Errorf("exec_lua surfaced in ListTools with gate off: %s", raw)
	}
}

// TestExecLuaGate_OnSurfaces verifies ListTools advertises exec_lua when the
// gate is on.
func TestExecLuaGate_OnSurfaces(t *testing.T) {
	s := NewMCPServer(NewManager(nullBridge{}, true))
	raw, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, tool := range got.Tools {
		if tool.Name == "exec_lua" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("exec_lua missing from ListTools with gate on: %s", raw)
	}
}

// TestExecLuaGate_OffBlocksCall verifies a direct Call("exec_lua", …) returns
// a gate-disabled error even if the agent guesses the tool name.
func TestExecLuaGate_OffBlocksCall(t *testing.T) {
	s := NewMCPServer(NewManager(nullBridge{}, false))
	out, _ := s.Call(context.Background(), "exec_lua", json.RawMessage(`{"lua":"return 1"}`))
	txt, isErr := parseResult(t, out)
	if !isErr {
		t.Fatalf("want isError on gated exec_lua call, got text=%q", txt)
	}
	if !strings.Contains(txt, "HAMMERSPOON_ALLOW_EXEC_LUA") {
		t.Errorf("want gate-disabled message, got %q", txt)
	}
}

// TestExecLuaGate_OnRoutes verifies a direct Call("exec_lua", …) reaches the
// bridge when the gate is on.
func TestExecLuaGate_OnRoutes(t *testing.T) {
	fb := &fakeBridge{envelope: Envelope{Ok: true, Result: json.RawMessage(`42`)}}
	s := NewMCPServer(NewManager(fb, true))
	out, _ := s.Call(context.Background(), "exec_lua",
		json.RawMessage(`{"lua":"return 42"}`))
	txt, isErr := parseResult(t, out)
	if isErr {
		t.Fatalf("unexpected isError: %s", txt)
	}
	if fb.callN != 1 {
		t.Errorf("bridge call count: want 1 got %d", fb.callN)
	}
	if fb.lastLua != "return 42" {
		t.Errorf("lua passthrough: got %q", fb.lastLua)
	}
}
