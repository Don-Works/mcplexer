package toolgate_test

import (
	"testing"

	"github.com/don-works/mcplexer/internal/toolgate"
)

func TestIsAdminTool(t *testing.T) {
	cases := []struct {
		name  string
		admin bool
	}{
		{toolgate.McpxPrefix + "search_tools", false},
		{toolgate.McpxPrefix + "execute_code", false},
		{toolgate.McpxPrefix + "provision_mcp", true},
		{"mcplexer__list_workspaces", true},
		{"mesh__send", false},
		{"github__create_issue", false},
		{"", false},
	}
	for _, c := range cases {
		if got := toolgate.IsAdminTool(c.name); got != c.admin {
			t.Errorf("IsAdminTool(%q) = %v, want %v", c.name, got, c.admin)
		}
	}
}

func TestAllowlistPatternGrantsAdmin(t *testing.T) {
	cases := []struct {
		pattern string
		admin   bool
	}{
		{"mcpx__execute_code", false},
		{"mcpx__search_tools", false},
		{"mcplexer__create_worker", true},
		{"mcplexer__*", true},
		{"mcpx__*", true},
		{"*", true},
		{"task__*", true},
		{"github__*", false},
	}
	for _, c := range cases {
		if got := toolgate.AllowlistPatternGrantsAdmin(c.pattern); got != c.admin {
			t.Errorf("AllowlistPatternGrantsAdmin(%q) = %v, want %v", c.pattern, got, c.admin)
		}
	}
}
