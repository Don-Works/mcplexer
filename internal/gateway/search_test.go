package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMatchesQuery(t *testing.T) {
	tests := []struct {
		name  string
		tool  Tool
		query string
		want  bool
	}{
		{
			name:  "name match",
			tool:  Tool{Name: "github__create_issue", Description: "Creates an issue"},
			query: "create",
			want:  true,
		},
		{
			name:  "description match",
			tool:  Tool{Name: "github__list_repos", Description: "List repositories"},
			query: "repositories",
			want:  true,
		},
		{
			name:  "no match",
			tool:  Tool{Name: "github__create_issue", Description: "Creates an issue"},
			query: "delete",
			want:  false,
		},
		{
			name:  "case insensitive name",
			tool:  Tool{Name: "GitHub__Create_Issue", Description: "Creates an issue"},
			query: "create_issue",
			want:  true,
		},
		{
			name:  "case insensitive description",
			tool:  Tool{Name: "github__list", Description: "List REPOSITORIES"},
			query: "repositories",
			want:  true,
		},
		{
			name:  "empty query matches all",
			tool:  Tool{Name: "anything", Description: "whatever"},
			query: "",
			want:  true,
		},
		// Multi-token fuzzy matching tests
		{
			name:  "multi-token matches across namespace and name",
			tool:  Tool{Name: "linear__list_tasks", Description: "List tasks from Linear"},
			query: "linear tasks",
			want:  true,
		},
		{
			name:  "multi-token matches namespace and description",
			tool:  Tool{Name: "linear__search_issues", Description: "Search issues and tasks in Linear"},
			query: "linear tasks",
			want:  true,
		},
		{
			name:  "multi-token all words must match",
			tool:  Tool{Name: "github__create_issue", Description: "Creates a GitHub issue"},
			query: "linear tasks",
			want:  false,
		},
		{
			name:  "multi-token matches with underscores expanded",
			tool:  Tool{Name: "slack__send_message", Description: "Send a message to a Slack channel"},
			query: "send message",
			want:  true,
		},
		{
			name:  "multi-token partial word match",
			tool:  Tool{Name: "jira__create_ticket", Description: "Create a Jira ticket"},
			query: "jira ticket",
			want:  true,
		},
		{
			name:  "multi-token with hyphens expanded",
			tool:  Tool{Name: "my-server__list-items", Description: "Lists items"},
			query: "list items",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesQuery(tt.tool, tt.query)
			if got != tt.want {
				t.Errorf("matchesQuery(%q, %q) = %v, want %v",
					tt.tool.Name, tt.query, got, tt.want)
			}
		})
	}
}

func TestScoreMatch(t *testing.T) {
	// Exact name match should score highest.
	exact := scoreMatch(Tool{Name: "linear__list_tasks", Description: "List tasks"}, "linear__list_tasks")
	partial := scoreMatch(Tool{Name: "linear__list_tasks", Description: "List tasks"}, "list")
	if exact <= partial {
		t.Errorf("exact name match (%d) should score higher than partial (%d)", exact, partial)
	}

	// Name match should score higher than description-only match.
	nameMatch := scoreMatch(Tool{Name: "github__create_issue", Description: "Creates an issue"}, "create_issue")
	descOnly := scoreMatch(Tool{Name: "github__list_repos", Description: "Create issue tracking"}, "create_issue")
	if nameMatch <= descOnly {
		t.Errorf("name match (%d) should score higher than desc-only (%d)", nameMatch, descOnly)
	}
}

func TestMarshalToolResult(t *testing.T) {
	result := marshalToolResult("hello world")
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tr.Content) != 1 {
		t.Fatalf("got %d content items, want 1", len(tr.Content))
	}
	if tr.Content[0].Type != "text" {
		t.Errorf("type = %q, want %q", tr.Content[0].Type, "text")
	}
	if tr.Content[0].Text != "hello world" {
		t.Errorf("text = %q, want %q", tr.Content[0].Text, "hello world")
	}
}

func TestBuiltinToolAnnotations(t *testing.T) {
	tests := []struct {
		name            string
		tool            Tool
		wantTitle       string
		wantReadOnly    *bool
		wantDestructive *bool
		wantOpenWorld   *bool
	}{
		{
			name:            "search_tools",
			tool:            searchToolsDefinition(),
			wantTitle:       "Search Tools",
			wantReadOnly:    boolPtr(true),
			wantDestructive: boolPtr(false),
			wantOpenWorld:   boolPtr(false),
		},
		{
			name:            "flush_cache",
			tool:            flushCacheToolDefinition(),
			wantTitle:       "Flush Cache",
			wantReadOnly:    nil,
			wantDestructive: boolPtr(false),
			wantOpenWorld:   boolPtr(false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, ok := tt.tool.Extras["annotations"]
			if !ok {
				t.Fatal("missing annotations in Extras")
			}

			var ann ToolAnnotations
			if err := json.Unmarshal(raw, &ann); err != nil {
				t.Fatalf("unmarshal annotations: %v", err)
			}

			if ann.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", ann.Title, tt.wantTitle)
			}
			assertBoolPtr(t, "readOnlyHint", ann.ReadOnlyHint, tt.wantReadOnly)
			assertBoolPtr(t, "destructiveHint", ann.DestructiveHint, tt.wantDestructive)
			assertBoolPtr(t, "openWorldHint", ann.OpenWorldHint, tt.wantOpenWorld)
		})
	}

	// Approval tools: list_pending has readOnly, approve/deny do not.
	approvalTools := approvalToolDefinitions()
	for _, tool := range approvalTools {
		t.Run(tool.Name, func(t *testing.T) {
			raw, ok := tool.Extras["annotations"]
			if !ok {
				t.Fatal("missing annotations in Extras")
			}
			var ann ToolAnnotations
			if err := json.Unmarshal(raw, &ann); err != nil {
				t.Fatalf("unmarshal annotations: %v", err)
			}
			assertBoolPtr(t, "destructiveHint", ann.DestructiveHint, boolPtr(false))
			assertBoolPtr(t, "openWorldHint", ann.OpenWorldHint, boolPtr(false))

			if tool.Name == "mcpx__list_pending_approvals" {
				assertBoolPtr(t, "readOnlyHint", ann.ReadOnlyHint, boolPtr(true))
			} else {
				assertBoolPtr(t, "readOnlyHint", ann.ReadOnlyHint, nil)
			}
		})
	}
}

func assertBoolPtr(t *testing.T, field string, got, want *bool) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s = %v, want nil", field, *got)
		}
		return
	}
	if got == nil {
		t.Errorf("%s = nil, want %v", field, *want)
		return
	}
	if *got != *want {
		t.Errorf("%s = %v, want %v", field, *got, *want)
	}
}

func TestSearchToolsDefinitionHasExpectedSchema(t *testing.T) {
	tool := searchToolsDefinition()
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if _, ok := schema.Properties["queries"]; !ok {
		t.Error("search_tools schema missing 'queries' property")
	}
	if _, ok := schema.Properties["limit"]; !ok {
		t.Error("search_tools schema missing 'limit' property")
	}
	if _, ok := schema.Properties["namespace"]; !ok {
		t.Error("search_tools schema missing singular 'namespace' compatibility alias")
	}
	// Neither should be required.
	for _, r := range schema.Required {
		t.Errorf("unexpected required field: %s", r)
	}
}

func TestFormatDiscoverAll(t *testing.T) {
	tools := []Tool{
		{
			Name:        "ns__tool1",
			Description: "First tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		},
		{Name: "ns__tool2", Description: "Second tool"},
	}
	result := formatDiscoverAll(tools)
	if !stringContains(result, "Found 2 tools") {
		t.Errorf("missing count header in: %s", result)
	}
	if !stringContains(result, "ns__tool1") {
		t.Error("missing tool1 in result")
	}
	if !stringContains(result, "ns__tool2") {
		t.Error("missing tool2 in result")
	}
	// Should include TypeScript code API.
	if !stringContains(result, "Code API") {
		t.Error("missing Code API section in result")
	}
	if !stringContains(result, "declare namespace") {
		t.Error("missing TypeScript declarations in result")
	}
}

func TestFormatDiscoverResults_MultiQuery(t *testing.T) {
	longDescription := "Create an issue with a very long description that should appear only as a compact one-line snippet in the per-query list before the TypeScript declarations are rendered.\n\nSecond paragraph should not be repeated in that list."
	results := []discoverQueryResult{
		{
			query: "slack",
			matches: []Tool{
				{Name: "slack__send_message", Description: "Send a message", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)},
			},
		},
		{
			query: "linear",
			matches: []Tool{
				{Name: "linear__create_issue", Description: longDescription, InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`)},
			},
		},
	}
	allMatched := map[string]Tool{
		"slack__send_message":  results[0].matches[0],
		"linear__create_issue": results[1].matches[0],
	}

	output := formatDiscoverResults(results, allMatched, 0)

	if !stringContains(output, `Results for "slack"`) {
		t.Error("missing slack query header")
	}
	if !stringContains(output, `Results for "linear"`) {
		t.Error("missing linear query header")
	}
	if !stringContains(output, "slack__send_message") {
		t.Error("missing slack tool")
	}
	if !stringContains(output, "linear__create_issue") {
		t.Error("missing linear tool")
	}
	queryList := strings.Split(output, "## Code API")[0]
	if stringContains(queryList, "Second paragraph should not be repeated") {
		t.Error("full result query list should use description snippets")
	}
	if !stringContains(output, "Code API") {
		t.Error("missing Code API section")
	}
	if !stringContains(output, "declare namespace slack") {
		t.Error("missing slack TypeScript namespace")
	}
	if !stringContains(output, "declare namespace linear") {
		t.Error("missing linear TypeScript namespace")
	}
}

func TestFormatDiscoverResults_Dedup(t *testing.T) {
	// Same tool matched by two queries should only appear once in TypeScript.
	sharedTool := Tool{
		Name:        "slack__send_message",
		Description: "Send a message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}
	results := []discoverQueryResult{
		{query: "slack", matches: []Tool{sharedTool}},
		{query: "message", matches: []Tool{sharedTool}},
	}
	allMatched := map[string]Tool{
		"slack__send_message": sharedTool,
	}

	output := formatDiscoverResults(results, allMatched, 0)

	// Count occurrences of "declare namespace slack" — should be exactly 1.
	count := 0
	for i := 0; i <= len(output)-len("declare namespace slack"); i++ {
		if output[i:i+len("declare namespace slack")] == "declare namespace slack" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 slack namespace declaration, got %d", count)
	}
}

func TestMatchesQuery_SearchTags(t *testing.T) {
	tool := Tool{
		Name:        "calendar__schedule_event",
		Description: "Schedule an event",
		Extras: map[string]json.RawMessage{
			"x-search-tags": json.RawMessage(`["meeting", "appointment", "booking"]`),
		},
	}

	if !matchesQuery(tool, "meeting") {
		t.Error("should match via search tag 'meeting'")
	}
	if !matchesQuery(tool, "appointment") {
		t.Error("should match via search tag 'appointment'")
	}
	if matchesQuery(tool, "dinosaur") {
		t.Error("should not match unrelated query")
	}
}

func TestMatchesQuery_StopWordFiltering(t *testing.T) {
	tool := Tool{
		Name:        "github__list_issues",
		Description: "List issues from GitHub",
	}

	// "show me the issues" → stop words removed → "issues" → should match
	if !matchesQuery(tool, "show me the issues") {
		t.Error("should match after filtering stop words: 'show me the issues'")
	}
}

func TestScoreMatch_SearchTagBoost(t *testing.T) {
	withTags := Tool{
		Name:        "calendar__schedule_event",
		Description: "Schedule an event",
		Extras: map[string]json.RawMessage{
			"x-search-tags": json.RawMessage(`["meeting", "appointment"]`),
		},
	}
	withoutTags := Tool{
		Name:        "calendar__schedule_event",
		Description: "Schedule an event",
	}

	scoreWith := scoreMatch(withTags, "meeting")
	scoreWithout := scoreMatch(withoutTags, "meeting")
	if scoreWith <= scoreWithout {
		t.Errorf("tagged tool should score higher (%d) than untagged (%d) for 'meeting'",
			scoreWith, scoreWithout)
	}
}

func TestFuzzyMatchTool(t *testing.T) {
	tools := []Tool{
		{Name: "github__create_issue"},
		{Name: "github__list_issues"},
		{Name: "slack__send_message"},
	}

	tests := []struct {
		name     string
		input    string
		wantOK   bool
		wantName string
	}{
		{
			name:     "normalized match (single underscore)",
			input:    "github_create_issue",
			wantOK:   true,
			wantName: "github__create_issue",
		},
		{
			name:     "normalized match (no underscore)",
			input:    "githubcreateissue",
			wantOK:   true,
			wantName: "github__create_issue",
		},
		{
			name:     "levenshtein match",
			input:    "github__creat_issue",
			wantOK:   true,
			wantName: "github__create_issue",
		},
		{
			name:   "too different, no match",
			input:  "completely_unrelated",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, ok := fuzzyMatchTool(tt.input, tools)
			if ok != tt.wantOK {
				t.Errorf("fuzzyMatchTool(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if ok && matched.Name != tt.wantName {
				t.Errorf("fuzzyMatchTool(%q) = %q, want %q", tt.input, matched.Name, tt.wantName)
			}
		})
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
	}
	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFilterStopWords(t *testing.T) {
	tokens := []string{"show", "me", "the", "issues"}
	filtered := filterStopWords(tokens)
	if len(filtered) != 1 || filtered[0] != "issues" {
		t.Errorf("filterStopWords(%v) = %v, want [issues]", tokens, filtered)
	}

	// All stop words → keep original.
	allStop := []string{"the", "a", "is"}
	kept := filterStopWords(allStop)
	if len(kept) != 3 {
		t.Errorf("all stop words should keep original, got %v", kept)
	}
}
