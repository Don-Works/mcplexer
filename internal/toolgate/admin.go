package toolgate

import (
	"path"
	"strings"
)

const (
	// McpxPrefix is the namespace for MCPlexer built-in tools.
	McpxPrefix = "mcpx__"
	// LegacyMcplexerPrefix is the legacy admin namespace.
	LegacyMcplexerPrefix = "mcplexer__"
)

// AdminMcpxTools enumerates mcpx__ names that require admin CWD context.
var AdminMcpxTools = map[string]bool{
	McpxPrefix + "provision_mcp":          true,
	McpxPrefix + "create_addon":           true,
	McpxPrefix + "import_openapi":         true,
	McpxPrefix + "approve_tool_call":      true,
	McpxPrefix + "deny_tool_call":         true,
	McpxPrefix + "list_pending_approvals": true,
	McpxPrefix + "reload_server":          true,
	McpxPrefix + "flush_cache":            true,
	McpxPrefix + "skill_install":          true,
}

// TaskAdminTools enumerates task__* names that require admin CWD context.
var TaskAdminTools = map[string]bool{
	"task__consolidate_statuses":       true,
	"task__apply_status_consolidation": true,
	"task__rebind_peer":                true,
}

// IsAdminTool reports whether a tool, by name, requires admin context.
func IsAdminTool(name string) bool {
	if name == "" {
		return false
	}
	if AdminMcpxTools[name] {
		return true
	}
	if TaskAdminTools[name] {
		return true
	}
	if strings.HasPrefix(name, LegacyMcplexerPrefix) {
		return true
	}
	if rest, ok := strings.CutPrefix(name, LegacyMcplexerPrefix); ok {
		return AdminMcpxTools[McpxPrefix+rest]
	}
	return false
}

// AllowlistPatternGrantsAdmin reports whether a delegation/worker allowlist
// entry (literal or glob) could match an admin-only tool. Delegated workers
// bypass the admin CWD gate for in-process calls, so these patterns must be
// rejected at delegation create time.
func AllowlistPatternGrantsAdmin(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if IsAdminTool(pattern) {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	for _, probe := range adminAllowlistProbes() {
		if ok, err := path.Match(pattern, probe); err == nil && ok {
			return true
		}
	}
	return false
}

func adminAllowlistProbes() []string {
	return []string{
		LegacyMcplexerPrefix + "create_worker",
		LegacyMcplexerPrefix + "list_workspaces",
		McpxPrefix + "provision_mcp",
		"task__consolidate_statuses",
		"task__apply_status_consolidation",
		"task__rebind_peer",
	}
}
