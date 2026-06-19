package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// helpTools is a small fixture toolset: two github tools and one memory tool,
// with descriptions + schemas so help('namespace') has signatures to render.
func helpTools() []ToolDef {
	return []ToolDef{
		{
			Name:        "github__list_issues",
			Description: "List issues in a repository.\nSecond paragraph ignored by help.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"owner":{"type":"string"}},"required":["owner"]}`),
		},
		{
			Name:        "github__get_repo",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "memory__recall",
			Description: "Recall memories matching a query.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		},
	}
}

// TestSandbox_HelpIndexListsNamespaces asserts a bare `help()` (no print
// wrapper) surfaces the namespace directory with tool counts, the global
// helpers, and the no-await reminder — the orientation a small model needs.
func TestSandbox_HelpIndexListsNamespaces(t *testing.T) {
	sandbox := NewSandbox(newMockCaller(), 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `help();`, helpTools())
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("help() must not error: %s", result.Error)
	}
	out := result.Output
	for _, want := range []string{
		"Available namespaces",
		"github (2 tool",
		"memory (1 tool",
		"Global helpers",
		"no await",
		"help('github')",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help() index missing %q, got:\n%s", want, out)
		}
	}
}

// TestSandbox_HelpNamespaceShowsSignatures asserts help('github') prints each
// tool as a copy-pasteable call signature with its lead-line description.
func TestSandbox_HelpNamespaceShowsSignatures(t *testing.T) {
	sandbox := NewSandbox(newMockCaller(), 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `help("github");`, helpTools())
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("help('github') must not error: %s", result.Error)
	}
	out := result.Output
	if !strings.Contains(out, "github.list_issues({owner") {
		t.Fatalf("expected list_issues signature with owner param, got:\n%s", out)
	}
	if !strings.Contains(out, "List issues in a repository.") {
		t.Fatalf("expected lead-line description, got:\n%s", out)
	}
	if strings.Contains(out, "Second paragraph") {
		t.Fatalf("help should only show the first description line, got:\n%s", out)
	}
	if !strings.Contains(out, "github.get_repo(") {
		t.Fatalf("expected get_repo (no-param) signature, got:\n%s", out)
	}
}

// TestSandbox_HelpUnknownNamespaceSuggests asserts a typo'd namespace arg
// (`help('guthub')`) gets a did-you-mean over the real namespaces rather
// than a silent empty result.
func TestSandbox_HelpUnknownNamespaceSuggests(t *testing.T) {
	sandbox := NewSandbox(newMockCaller(), 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `help("guthub");`, helpTools())
	if err != nil {
		t.Fatal(err)
	}
	out := result.Output
	if !strings.Contains(out, "No namespace") {
		t.Fatalf("expected unknown-namespace notice, got:\n%s", out)
	}
	if !strings.Contains(out, "Did you mean") || !strings.Contains(out, "github") {
		t.Fatalf("expected did-you-mean suggesting github, got:\n%s", out)
	}
}

// TestSandbox_HelpWhitespaceArgument asserts a whitespace-only argument is
// trimmed and treated like the no-arg form (the index), not an error or a
// bogus "no namespace '   '" lookup — a small model might pass a stray space.
func TestSandbox_HelpWhitespaceArgument(t *testing.T) {
	sandbox := NewSandbox(newMockCaller(), 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `help("   ");`, helpTools())
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("help('   ') must not error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "Available namespaces") {
		t.Fatalf("whitespace arg should show the index, got:\n%s", result.Output)
	}
	if strings.Contains(result.Output, "No namespace") {
		t.Fatalf("whitespace arg must not be treated as a namespace lookup, got:\n%s", result.Output)
	}
}

// TestSandbox_HelpEmptyToolset asserts help() still produces useful output
// (helpers + guidance) when no tool namespaces are registered.
func TestSandbox_HelpEmptyToolset(t *testing.T) {
	sandbox := NewSandbox(newMockCaller(), 5*time.Second)
	result, err := sandbox.Execute(context.Background(), `help();`, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := result.Output
	if !strings.Contains(out, "No tool namespaces are registered") {
		t.Fatalf("expected empty-toolset notice, got:\n%s", out)
	}
	if !strings.Contains(out, "Global helpers") {
		t.Fatalf("expected global helpers even with no tools, got:\n%s", out)
	}
}
