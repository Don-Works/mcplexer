package control

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
)

// callResult decodes the standard MCP text-content envelope a control
// handler returns into a (text, isError) pair.
func callResult(t *testing.T, raw json.RawMessage) (string, bool) {
	t.Helper()
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, raw)
	}
	if len(env.Content) == 0 {
		return "", env.IsError
	}
	return env.Content[0].Text, env.IsError
}

func TestCallBrain_UnavailableErrors(t *testing.T) {
	b := NewInternalBackend(newTestDB(t), nil) // no brain git wired
	ctx := context.Background()

	for _, tool := range []string{"brain_push", "brain_status"} {
		out, err := b.Call(ctx, tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("%s Call returned transport error: %v", tool, err)
		}
		text, isErr := callResult(t, out)
		if !isErr {
			t.Fatalf("%s with no brain git should be an error result, got %q", tool, text)
		}
		if !strings.Contains(text, "not available") {
			t.Fatalf("%s error text = %q, want 'not available'", tool, text)
		}
	}
}

func TestCallBrain_Status(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not on PATH")
	}
	dir := t.TempDir()
	g := brain.NewGit(dir, nil)
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("brain git init: %v", err)
	}

	b := NewInternalBackend(newTestDB(t), nil)
	b.SetBrainGit(g)

	out, err := b.Call(context.Background(), "brain_status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call brain_status: %v", err)
	}
	text, isErr := callResult(t, out)
	if isErr {
		t.Fatalf("brain_status unexpected error: %s", text)
	}
	var st brain.GitStatus
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		t.Fatalf("decode GitStatus: %v\n%s", err, text)
	}
	if !st.Initialized {
		t.Fatal("expected initialised repo")
	}
	if st.LastCommit == "" {
		t.Fatal("expected a last-commit subject")
	}
}

// TestBrainPush_NoRemoteSurfacesError confirms a push with no configured
// remote returns a structured error (not a panic), exercising the
// pull-then-push path end-to-end against a real repo.
func TestBrainPush_NoRemoteSurfacesError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not on PATH")
	}
	dir := t.TempDir()
	g := brain.NewGit(dir, nil)
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("brain git init: %v", err)
	}
	b := NewInternalBackend(newTestDB(t), nil)
	b.SetBrainGit(g)

	out, err := b.Call(context.Background(), "brain_push", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call brain_push: %v", err)
	}
	text, isErr := callResult(t, out)
	if !isErr {
		t.Fatalf("brain_push with no upstream should error, got %q", text)
	}
}
