package delegscope

import (
	"encoding/json"
	"testing"
)

func TestIsDefaultAllowlist(t *testing.T) {
	// A reordered copy of the execute default: same set, different order.
	reordered := func(raw string) string {
		var names []string
		if err := json.Unmarshal([]byte(raw), &names); err != nil {
			t.Fatalf("seed unmarshal: %v", err)
		}
		if len(names) < 2 {
			t.Fatalf("seed too short to reorder: %d", len(names))
		}
		names[0], names[len(names)-1] = names[len(names)-1], names[0]
		out, err := json.Marshal(names)
		if err != nil {
			t.Fatalf("seed marshal: %v", err)
		}
		return string(out)
	}
	// The execute default minus its last tool: a genuine (narrower) operator scope.
	minusOne := func(raw string) string {
		var names []string
		if err := json.Unmarshal([]byte(raw), &names); err != nil {
			t.Fatalf("seed unmarshal: %v", err)
		}
		out, err := json.Marshal(names[:len(names)-1])
		if err != nil {
			t.Fatalf("seed marshal: %v", err)
		}
		return string(out)
	}

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "execute default verbatim", raw: DefaultToolsJSON, want: true},
		{name: "review default verbatim", raw: DefaultReviewToolsJSON, want: true},
		{name: "execute default reordered", raw: reordered(DefaultToolsJSON), want: true},
		{name: "execute default with whitespace", raw: "  " + DefaultToolsJSON + "\n", want: true},
		{name: "execute default minus one tool", raw: minusOne(DefaultToolsJSON), want: false},
		{name: "superset of the default", raw: `["mcpx__execute_code","task__create","some__extra_tool_not_in_default"]`, want: false},
		{name: "unrelated restrictive allowlist", raw: `["task__create"]`, want: false},
		{name: "empty array (sqlite default)", raw: "[]", want: false},
		{name: "empty string", raw: "", want: false},
		{name: "whitespace only", raw: "   ", want: false},
		{name: "json null", raw: "null", want: false},
		{name: "malformed json", raw: "{not valid", want: false},
		{name: "object not array", raw: `{"preset":"coder"}`, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDefaultAllowlist(tc.raw); got != tc.want {
				t.Errorf("IsDefaultAllowlist(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestDefaultsAreDistinctAndParseable guards the two constants: they must be
// valid JSON string arrays and must not be equal as sets (the review default is
// a strict narrowing of the execute default).
func TestDefaultsAreDistinctAndParseable(t *testing.T) {
	exec, ok := parseToolList(DefaultToolsJSON)
	if !ok {
		t.Fatal("DefaultToolsJSON did not parse as a tool list")
	}
	review, ok := parseToolList(DefaultReviewToolsJSON)
	if !ok {
		t.Fatal("DefaultReviewToolsJSON did not parse as a tool list")
	}
	if equalStringSets(exec, review) {
		t.Fatal("execute and review defaults are the same set; review must be narrower")
	}
	if len(review) >= len(exec) {
		t.Errorf("review default (%d tools) is not narrower than execute default (%d tools)", len(review), len(exec))
	}
}
