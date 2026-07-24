package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/pathguard"
)

type workerFilesystemScopeKey struct{}

// WithWorkerFilesystemScope attaches a canonical runtime filesystem boundary
// to a worker call. Callers must fail closed when construction fails.
func WithWorkerFilesystemScope(
	ctx context.Context, root, workingDir string, claims []string,
) (context.Context, error) {
	scope, err := pathguard.New(root, workingDir, claims)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, workerFilesystemScopeKey{}, scope), nil
}

func workerFilesystemScopeFromContext(ctx context.Context) (pathguard.Scope, bool) {
	if ctx == nil {
		return pathguard.Scope{}, false
	}
	scope, ok := ctx.Value(workerFilesystemScopeKey{}).(pathguard.Scope)
	return scope, ok
}

// isolatedWorkerSafeTools is deliberately exact. Worktree mode does not try
// to infer arbitrary downstream path contracts from argument names. Local
// filesystem access goes only through the gateway-owned workspace tools;
// selected task/memory/mesh/skill contracts remain available because they do
// not execute local paths.
var isolatedWorkerSafeTools = map[string]struct{}{
	"mcpx__search_tools":             {},
	"mcpx__call_tool":                {},
	"mcpx__execute_code":             {},
	"mcpx__retrieve":                 {},
	"mcpx__skill_search":             {},
	"mcpx__skill_get":                {},
	"mcpx__workspace_read_file":      {},
	"mcpx__workspace_list_directory": {},
	"mcpx__workspace_write_file":     {},
	"mcpx__workspace_edit_file":      {},
	"mesh__send":                     {},
	"mesh__receive":                  {},
	"mesh__list_peers":               {},
	"mesh__list_agents":              {},
	"memory__save":                   {},
	"memory__recall":                 {},
	"memory__list":                   {},
	"task__create":                   {},
	"task__get":                      {},
	"task__list":                     {},
	"task__update":                   {},
	"task__append_note":              {},
}

func isolatedWorkerToolAllowed(name string) bool {
	_, ok := isolatedWorkerSafeTools[name]
	return ok
}

func guardWorkerFilesystemToolName(ctx context.Context, toolName string) error {
	if _, isolated := workerFilesystemScopeFromContext(ctx); !isolated {
		return nil
	}
	if !isolatedWorkerToolAllowed(toolName) {
		return fmt.Errorf("tool %q is unavailable under worktree isolation; use an exact mcpx__workspace_* contract or explicitly select worker_isolation=none", toolName)
	}
	return nil
}

func guardWorkerFilesystemRoute(ctx context.Context, toolName, downstreamID string) error {
	if _, isolated := workerFilesystemScopeFromContext(ctx); !isolated {
		return nil
	}
	expected := ""
	switch {
	case strings.HasPrefix(toolName, "mcpx__"):
		expected = "mcpx-builtin"
	case strings.HasPrefix(toolName, "mesh__"):
		expected = "mesh-builtin"
	case strings.HasPrefix(toolName, "memory__"):
		expected = "memory-builtin"
	case strings.HasPrefix(toolName, "task__"):
		expected = "task-builtin"
	case strings.HasPrefix(toolName, "skill__"):
		expected = "skill-builtin"
	}
	if expected == "" || downstreamID != expected {
		return fmt.Errorf("tool %q resolved to untrusted route %q under worktree isolation", toolName, downstreamID)
	}
	return nil
}

func filterByWorkerFilesystemContract(ctx context.Context, tools []Tool) []Tool {
	if _, isolated := workerFilesystemScopeFromContext(ctx); !isolated {
		return tools
	}
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if isolatedWorkerToolAllowed(tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}

// guardWorkerFilesystemArgs is the authoritative exact-contract gate at the
// concrete inner-call boundary. It never rewrites paths or canonicalizes
// fields on remote APIs. Unknown/mixed local contracts require the explicit
// worker_isolation:none escape hatch.
func guardWorkerFilesystemArgs(
	ctx context.Context, toolName string, raw json.RawMessage,
) (json.RawMessage, error) {
	if _, isolated := workerFilesystemScopeFromContext(ctx); !isolated {
		return raw, nil
	}
	if err := guardWorkerFilesystemToolName(ctx, toolName); err != nil {
		return nil, err
	}
	if strings.HasPrefix(toolName, "mesh__") {
		var args struct {
			WorkspacePath string `json:"workspace_path"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &args) == nil && args.WorkspacePath != "" {
			return nil, fmt.Errorf("tool %q cannot accept workspace_path under worktree isolation", toolName)
		}
	}
	return raw, nil
}
