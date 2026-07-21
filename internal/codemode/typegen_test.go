package codemode

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateTypeScript_SimpleTypes(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "github__list_issues",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"owner": {"type": "string"},
					"repo": {"type": "string"},
					"state": {"type": "string", "enum": ["open", "closed", "all"]}
				},
				"required": ["owner", "repo"]
			}`),
		},
		{
			Name: "github__create_issue",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"owner": {"type": "string"},
					"repo": {"type": "string"},
					"title": {"type": "string"},
					"body": {"type": "string"}
				},
				"required": ["owner", "repo", "title"]
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "declare namespace github") {
		t.Error("expected namespace github")
	}
	if !strings.Contains(ts, "interface ListIssuesParams") {
		t.Error("expected ListIssuesParams interface")
	}
	if !strings.Contains(ts, "owner: string") {
		t.Error("expected owner field")
	}
	if !strings.Contains(ts, `state?: "open" | "closed" | "all"`) {
		t.Error("expected state enum field")
	}
	if !strings.Contains(ts, "function list_issues(params: ListIssuesParams): any") {
		t.Error("expected list_issues function")
	}
	if !strings.Contains(ts, "declare function print(value: any): void") {
		t.Error("expected print declaration")
	}
	if !strings.Contains(ts, "(any | null)[]") {
		t.Error("expected parallel return type to include (any | null)[]")
	}
	if !strings.Contains(ts, "declare function compact(value: any): any") {
		t.Error("expected compact declaration")
	}
}

func TestGenerateTypeScript_MultipleNamespaces(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "github__list_repos",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"org": {"type": "string"}},
				"required": ["org"]
			}`),
		},
		{
			Name: "linear__list_issues",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"team_id": {"type": "string"}},
				"required": ["team_id"]
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "declare namespace github") {
		t.Error("expected github namespace")
	}
	if !strings.Contains(ts, "declare namespace linear") {
		t.Error("expected linear namespace")
	}
}

func TestGenerateTypeScript_NoParams(t *testing.T) {
	tools := []ToolDef{
		{
			Name:        "github__whoami",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "function whoami(): any") {
		t.Error("expected no-params function")
	}
}

func TestGenerateTypeScript_PunctuatedMemberGetsCallableAlias(t *testing.T) {
	ts := GenerateTypeScript([]ToolDef{{
		Name: "gcal__get-current-time",
		InputSchema: json.RawMessage(`{
			"type":"object","properties":{"account":{"type":"string"}}
		}`),
	}})
	for _, want := range []string{
		"interface GetCurrentTimeParams",
		"function get_current_time(params: GetCurrentTimeParams): any",
		"Code Mode alias for MCP member `get-current-time`",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("generated API missing %q:\n%s", want, ts)
		}
	}
	if strings.Contains(ts, "function get-current-time") {
		t.Fatalf("generated invalid JavaScript member syntax:\n%s", ts)
	}
}

func TestGenerateTypeScript_ArrayType(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "github__add_labels",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"labels": {"type": "array", "items": {"type": "string"}}
				},
				"required": ["labels"]
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "labels: string[]") {
		t.Error("expected string[] type")
	}
}

func TestGenerateTypeScript_NestedObject(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "api__create",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"config": {
						"type": "object",
						"properties": {
							"name": {"type": "string"},
							"count": {"type": "integer"}
						},
						"required": ["name"]
					}
				},
				"required": ["config"]
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "config: { count?: number; name: string }") {
		t.Errorf("expected inline object type, got:\n%s", ts)
	}
}

func TestGenerateTypeScript_NumberTypes(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "api__query",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"limit": {"type": "integer"},
					"score": {"type": "number"}
				}
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "limit?: number") {
		t.Error("expected integer → number")
	}
	if !strings.Contains(ts, "score?: number") {
		t.Error("expected number type")
	}
}

func TestGenerateTypeScript_BooleanType(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "api__toggle",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"enabled": {"type": "boolean"}
				}
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	if !strings.Contains(ts, "enabled?: boolean") {
		t.Error("expected boolean type")
	}
}

func TestToPascalCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"list_issues", "ListIssues"},
		{"create", "Create"},
		{"get_pr_comments", "GetPrComments"},
		{"whoami", "Whoami"},
	}

	for _, tt := range tests {
		got := toPascalCase(tt.input)
		if got != tt.want {
			t.Errorf("toPascalCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestJsonSchemaToTS_AnyOf(t *testing.T) {
	raw := json.RawMessage(`{
		"anyOf": [
			{"type": "string"},
			{"type": "number"}
		]
	}`)

	got := jsonSchemaToTS(raw)
	if got != "string | number" {
		t.Errorf("expected 'string | number', got %q", got)
	}
}

func TestGenerateTypeScript_ToolDescription(t *testing.T) {
	tools := []ToolDef{
		{
			Name:        "postgres__query",
			Description: "Run a read-only SQL query. SELECT queries only.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"sql": {"type": "string", "description": "SELECT SQL query to execute"}
				},
				"required": ["sql"]
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	// Check field JSDoc.
	if !strings.Contains(ts, "/** SELECT SQL query to execute */") {
		t.Errorf("expected field JSDoc, got:\n%s", ts)
	}

	// Check function JSDoc.
	if !strings.Contains(ts, "Run a read-only SQL query") {
		t.Errorf("expected tool description in JSDoc, got:\n%s", ts)
	}
}

func TestGenerateTypeScript_DescriptionAndExamples(t *testing.T) {
	tools := []ToolDef{
		{
			Name:        "github__list_repos",
			Description: "List repositories for an organization.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"org": {"type": "string"}},
				"required": ["org"]
			}`),
			Examples: []string{`github.list_repos({org: "acme"})`},
		},
	}

	ts := GenerateTypeScript(tools)

	// Description and example should both appear in the function JSDoc.
	if !strings.Contains(ts, "List repositories for an organization.") {
		t.Errorf("expected description in JSDoc, got:\n%s", ts)
	}
	if !strings.Contains(ts, `@example github.list_repos({org: "acme"})`) {
		t.Errorf("expected @example in JSDoc, got:\n%s", ts)
	}
}

func TestGenerateTypeScript_NoDescriptionNoJSDoc(t *testing.T) {
	tools := []ToolDef{
		{
			Name: "api__ping",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
	}

	ts := GenerateTypeScript(tools)

	// No description or examples → no JSDoc block.
	if strings.Contains(ts, "/**") && !strings.Contains(ts, "Print output") {
		// Only the global print/parallel helpers should have JSDoc.
		lines := strings.Split(ts, "\n")
		for _, line := range lines {
			if strings.Contains(line, "/**") &&
				!strings.Contains(line, "Print output") &&
				!strings.Contains(line, "Execute multiple") {
				t.Errorf("unexpected JSDoc block for tool without description: %s", line)
			}
		}
	}
}
