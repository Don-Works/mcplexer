package main

import (
	"encoding/json"
	"testing"
)

func TestMaybeInjectRoots_Initialize_NoRoots(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`
	cwd := "/tmp/test/gateway"

	got := maybeInjectRoots([]byte(input), cwd)

	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(got, &msg); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}

	rootsRaw, ok := params["roots"]
	if !ok {
		t.Fatal("expected roots to be injected")
	}

	var roots []struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(rootsRaw, &roots); err != nil {
		t.Fatalf("unmarshal roots: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("expected 1 root, got %d", len(roots))
	}
	if roots[0].URI != "file:///tmp/test/gateway" {
		t.Errorf("unexpected root URI: %s", roots[0].URI)
	}
}

func TestMaybeInjectRoots_Initialize_ExistingRoots(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"roots":[{"uri":"file:///existing"}]}}`
	cwd := "/some/path"

	got := maybeInjectRoots([]byte(input), cwd)

	if string(got) != input {
		t.Errorf("expected line unchanged when roots already present\ngot:  %s\nwant: %s", got, input)
	}
}

func TestMaybeInjectRoots_NonInitialize(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	cwd := "/some/path"

	got := maybeInjectRoots([]byte(input), cwd)

	if string(got) != input {
		t.Error("expected non-initialize message to pass through unchanged")
	}
}

func TestMaybeInjectRoots_EmptyCwd(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

	got := maybeInjectRoots([]byte(input), "")

	if string(got) != input {
		t.Error("expected empty cwd to pass through unchanged")
	}
}

func TestMaybeInjectRoots_InvalidJSON(t *testing.T) {
	input := `not json at all`
	cwd := "/some/path"

	got := maybeInjectRoots([]byte(input), cwd)

	if string(got) != input {
		t.Error("expected invalid JSON to pass through unchanged")
	}
}
