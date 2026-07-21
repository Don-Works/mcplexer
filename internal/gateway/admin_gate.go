package gateway

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/toolgate"
)

// mcplexerModulePath is the Go module declaration that uniquely
// identifies the mcplexer source repo (or a worktree under one). When
// a session's CWD is at or under a directory whose go.mod declares
// this module, the admin CWD gate lifts so a developer working on the
// gateway code itself can exercise the admin tools without leaving the
// project directory. Mirrors the dev-mode escape hatch in
// ~/.claude/hooks/block-mcplexer-db.sh — keep the two in lock-step.
const mcplexerModulePath = "github.com/don-works/mcplexer"

// devRepoCache memoises whether a given (cleaned, abs) CWD resolves to
// a mcplexer source repo. Keyed by CWD, value is bool. tools/list can
// be called many times per session; the underlying filesystem check is
// cheap but not free, and the answer is stable for the lifetime of a
// session. We never invalidate — a developer who checks out a non-
// mcplexer branch and expects the gate to slam shut should restart
// their session anyway.
var devRepoCache sync.Map // map[string]bool

// AdminCWDGate decides whether the agent's session sees admin tools.
//
// The product rule is: admin tools (CRUD over workspaces/routes/servers/
// auth-scopes/secrets, plus mcpx__provision_mcp / create_addon /
// import_openapi / approve_tool_call / deny_tool_call /
// list_pending_approvals / reload_server / flush_cache) are visible
// from two contexts:
//
//  1. The data directory (default ~/.mcplexer) and any path under it.
//  2. A mcplexer source repo (any directory whose go.mod declares
//     `github.com/don-works/mcplexer`) and paths under it — so a
//     developer working on the gateway code can exercise admin tools
//     in-place. Mirrors the dev-mode lift in the layer-2 hook
//     (~/.claude/hooks/block-mcplexer-db.sh).
//
// Everywhere else, the agent sees only the universal surface —
// search_tools, execute_code, secret__prompt, mesh__send,
// mesh__receive — and any addon-namespaced tools the user has
// explicitly routed to that workspace.
//
// Rationale: agents working in a project directory should not have
// authority to mutate gateway configuration. Configuration belongs to
// the dedicated admin context. This is the directory-scoped routing
// principle applied to the gateway's own control surface.
type AdminCWDGate struct {
	// dataDir is the absolute, cleaned path that marks the admin context.
	dataDir string
}

// NewAdminCWDGate returns a gate that treats `dataDir` (and any path
// inside it) as the admin context. An empty dataDir disables the gate
// entirely — every tool is visible. That degrades open rather than
// closed; tests rely on the default-empty being permissive, but
// production wiring always passes a real path from cfg.DBDSN's parent.
func NewAdminCWDGate(dataDir string) *AdminCWDGate {
	adminBypassWarnOnce.Do(WarnIfAdminCWDBypass)
	return &AdminCWDGate{dataDir: filepath.Clean(dataDir)}
}

// adminBypassWarnOnce ensures the dev break-glass startup warning is
// emitted at most once per process, regardless of how many gates are
// constructed across the daemon's boot paths.
var adminBypassWarnOnce sync.Once

// Enabled reports whether the gate has a configured data directory and
// will actually filter tools. Returns false if dataDir was empty.
func (g *AdminCWDGate) Enabled() bool {
	return g != nil && g.dataDir != "" && g.dataDir != "."
}

// IsAdminCWD reports whether `cwd` is at or under the data directory and
// therefore allowed to see admin tools.
//
// Dev-mode escape hatch: a CWD at or under a mcplexer source repo (any
// directory whose go.mod declares `github.com/don-works/mcplexer`) also
// passes. This lets a developer working on the gateway code itself
// exercise admin tools from the source tree without `cd ~/.mcplexer`.
// Mirrors the layer-2 file-access hook so the asymmetry between "can
// edit the DB from the source repo" and "can call admin tools from the
// source repo" goes away.
func (g *AdminCWDGate) IsAdminCWD(cwd string) bool {
	if !g.Enabled() {
		return true // no gate configured — pass everything through.
	}
	// Dev break-glass: MCPLEXER_ADMIN_ALLOW_ANY_CWD=1 lifts the CWD gate
	// entirely so a developer can exercise admin tools from any working
	// directory. SECURITY: this disables the prompt-injection trust
	// boundary that normally prevents an agent in an untrusted project dir
	// from reconfiguring the gateway / reading secrets. Opt-in, default
	// off; a one-time startup warning is emitted by WarnIfAdminCWDBypass.
	if adminAnyCWDBypass() {
		return true
	}
	if cwd == "" {
		return false
	}
	cleaned := filepath.Clean(cwd)
	if cleaned == g.dataDir {
		return true
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(cleaned, g.dataDir+sep) {
		return true
	}
	return isMcplexerSourceCWD(cleaned)
}

// IsAdminContext is the socket-aware admin decision. It lifts the gate
// when ANY available signal resolves to the admin context:
//
//   - the client-reported CWD (`cwd`) — the stdio path, where
//     os.Getwd() is trustworthy; or
//   - any registered workspace root (`workspaceRoots`) whose directory
//     tree declares the mcplexer module — the daemon/socket path, where
//     Claude Code over the socket doesn't advertise the source repo as
//     an MCP root so `cwd` is empty.
//
// The trust decision is unchanged from IsAdminCWD: a path qualifies only
// if it's inside the data dir or inside a tree whose go.mod declares
// `github.com/don-works/mcplexer`. Workspace roots are checked ONLY
// against the go.mod-module rule — never the data-dir prefix and never a
// path-name heuristic — because a workspace root pointing at the source
// repo is the same "owns the path == owns the host" trust signal a
// source CWD carries. We do not widen the boundary, only the set of
// signals that can satisfy it.
func (g *AdminCWDGate) IsAdminContext(cwd string, workspaceRoots []string) bool {
	if !g.Enabled() {
		return true // no gate configured — pass everything through.
	}
	if g.IsAdminCWD(cwd) {
		return true
	}
	for _, root := range workspaceRoots {
		if root == "" {
			continue
		}
		if isMcplexerSourceCWD(filepath.Clean(root)) {
			return true
		}
	}
	return false
}

// adminAnyCWDBypass reports whether the MCPLEXER_ADMIN_ALLOW_ANY_CWD dev
// break-glass is set. When true, the admin CWD gate lifts from every
// working directory. Opt-in, default off.
func adminAnyCWDBypass() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCPLEXER_ADMIN_ALLOW_ANY_CWD"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// WarnIfAdminCWDBypass emits a one-time startup warning when the dev
// break-glass is active, so an operator who left it on is reminded that
// the CWD trust boundary is disabled. Call once during daemon boot.
func WarnIfAdminCWDBypass() {
	if adminAnyCWDBypass() {
		slog.Warn("MCPLEXER_ADMIN_ALLOW_ANY_CWD is set — admin tools are exposed from EVERY working directory; the prompt-injection trust boundary is DISABLED. Unset this env var in production.")
	}
}

// isMcplexerSourceCWD reports whether `cwd` is inside a directory tree
// rooted at a go.mod that declares the mcplexer module path. Walks up
// from cwd to the filesystem root looking for a go.mod, then verifies
// the module declaration. Result is memoised per input CWD.
//
// Returning true here is a deliberate trust decision: anyone who can
// drop a go.mod containing `module github.com/don-works/mcplexer` into a
// directory has already won by other means (write access to that path
// implies they can also write ~/.mcplexer). The check is a
// dev-ergonomics affordance, not a security boundary — the boundary is
// still "you must own the host filesystem".
func isMcplexerSourceCWD(cwd string) bool {
	if cwd == "" || cwd == "." {
		return false
	}
	if v, ok := devRepoCache.Load(cwd); ok {
		return v.(bool)
	}
	result := false
	dir := cwd
	for {
		if hasMcplexerGoMod(filepath.Join(dir, "go.mod")) {
			result = true
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir { // hit root
			break
		}
		dir = parent
	}
	devRepoCache.Store(cwd, result)
	return result
}

// hasMcplexerGoMod returns true iff `path` is a readable file whose
// first non-blank, non-comment line declares the mcplexer module. The
// "module" directive is required to be on a single line in module
// mode, so a prefix scan is sufficient — we cap at 4KB to avoid
// reading large files that happen to be misnamed go.mod.
func hasMcplexerGoMod(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 4096 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if line == "module "+mcplexerModulePath {
			return true
		}
		// First non-comment line that isn't our module decl — bail.
		// go.mod requires the module line first, so we don't need to
		// scan further.
		if strings.HasPrefix(line, "module ") {
			return false
		}
		return false
	}
	return false
}

// IsAdminTool reports whether a tool, by name, requires admin context.
// Classification lives in internal/toolgate so delegation allowlist
// validation can share the same rules without an import cycle.
func IsAdminTool(name string) bool {
	return toolgate.IsAdminTool(name)
}

// taskAdminTools is the gateway-local alias kept so handler_tasks_admin
// stays in lock-step with the CWD gate. Points at the shared map in
// toolgate — do not duplicate entries here.
var taskAdminTools = toolgate.TaskAdminTools

// FilterAdminTools returns the subset of `tools` visible from the given
// CWD and workspace roots. Admin tools are dropped when the gate is
// enabled and neither the CWD nor any workspace root resolves to the
// admin context (data dir or mcplexer source tree). Passing nil for
// workspaceRoots reduces this to the CWD-only decision.
func (g *AdminCWDGate) FilterAdminTools(tools []Tool, cwd string, workspaceRoots []string) []Tool {
	if g.IsAdminContext(cwd, workspaceRoots) {
		return tools
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if IsAdminTool(t.Name) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// inProcessWorkerCallKey marks a context as "this tool call was
// dispatched by the in-process worker runner". Workers are configured
// + audited by the gateway operator; their identity has already been
// established by the time the runner picks them up. They run inside
// the daemon, never crossing the JSON-RPC boundary that the CWD gate
// is designed to protect against. The flag lets the admin gate skip
// the CWD check for these calls without weakening the external-client
// guarantee — external JSON-RPC calls never see this flag.
type inProcessWorkerCallKey struct{}

// WithInProcessWorkerCall marks ctx as originating from a trusted in-
// process worker dispatch. The wiring layer (cmd/mcplexer's
// workerBuiltinAdapter) attaches this before calling Server.CallTool;
// the admin gate honours it as a CWD-bypass signal.
func WithInProcessWorkerCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, inProcessWorkerCallKey{}, true)
}

// IsInProcessWorkerCall reports whether ctx was marked by
// WithInProcessWorkerCall. False on every external JSON-RPC path.
func IsInProcessWorkerCall(ctx context.Context) bool {
	v, _ := ctx.Value(inProcessWorkerCallKey{}).(bool)
	return v
}
