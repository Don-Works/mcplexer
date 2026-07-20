package runner

import (
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

// ErrCLIScopeUnenforceable is returned when a CLI-provider worker carries
// scope configuration (tool_allowlist_json / capability_profile_json) that
// the gateway cannot apply to that worker's actual tool calls.
var ErrCLIScopeUnenforceable = errors.New("cli worker scope is unenforceable")

// Why a CLI worker's configured scope cannot be enforced today
//
// An API-provider worker dispatches every tool call back through the runner:
// loop -> ToolDispatcher.DispatchTool -> withWorkerAllowlist, which attaches
// gateway.WithWorkerToolAllowlist + gateway.WithWorkerCapabilityProfile to the
// call context. The gateway reads those context values at its dispatch
// chokepoint (checkWorkerToolAllowlist / checkWorkerCapability) and the scope
// applies.
//
// A CLI-provider worker does not. No CLI adapter ever returns ToolCalls —
// claude_cli, opencode_cli, grok_cli, mimo_cli, gemini_cli, codex_cli and
// pi_cli all hand the whole agent loop to an external process and return only
// final text. That child reaches mcplexer the way any harness does: a fresh
// MCP session over the daemon's local socket. A fresh session carries none of
// the run's context values, so the profile the gateway looks for is nil and
// nil means allow-all (the correct default for the operator's own sessions).
//
// Net effect: for a CLI worker, tool_allowlist_json and
// capability_profile_json gate ONLY the pre/post-execute hook scripts, which
// hooks.go dispatches through the runner. Every tool call the agent actually
// makes runs unscoped. An operator who sets capability_preset "researcher" on
// a claude_cli delegation gets a child that can still write.
//
// There is no in-band fix. The child holds a shell, the daemon socket is
// reachable at a well-known path outside the sandbox deny list, and every
// carrier the runner controls at spawn time — environment, CWD, the MCP client
// config — is presented BY the child, so an unscoped session is always one
// re-connect away. Enforcement needs the run identity to be an ambient
// property of the connection (a per-run endpoint the child cannot bypass) —
// the same missing piece prepareRun already names when it refuses isolated
// CLI delegations.
//
// Until that lands, fail closed: refuse the run rather than execute an
// unscoped child under a scoped worker's configuration. Refusing is narrow —
// it fires only when an operator explicitly asked for a scope — and it is the
// same shape as the isolation refusal it sits beside.
func cliScopeUnenforceable(worker *store.Worker) error {
	if worker == nil || !models.IsCLIProvider(worker.ModelProvider) {
		return nil
	}
	var requested []string
	if workerAllowlistScopeSet(worker.ToolAllowlistJSON) {
		requested = append(requested, "tool_allowlist_json")
	}
	if workerCapabilityScopeSet(worker.CapabilityProfileJSON) {
		requested = append(requested, "capability_profile_json")
	}
	if len(requested) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: model_provider %q runs the agent loop in an external CLI whose MCP "+
			"session reaches the gateway without this run's scope, so %s would gate "+
			"only the pre/post-execute hooks and not the agent's own tool calls; "+
			"clear the scope columns to run the worker unscoped, or use an API "+
			"provider (anthropic, openai, openai_compat) whose tool calls dispatch "+
			"through the runner",
		ErrCLIScopeUnenforceable, worker.ModelProvider, strings.Join(requested, " + "),
	)
}

// workerAllowlistScopeSet reports whether tool_allowlist_json carries an
// operator-authored allowlist.
//
// "[]" is deliberately NOT treated as a request even though the enforcement
// side reads it as deny-everything: store/sqlite applyWorkerDefaults backfills
// every worker's column with "[]" when it is left empty, so "[]" is the
// storage default and carries no operator intent. Reading it as a request
// would refuse every CLI worker ever created, which is a far bigger blast
// radius than the hole this guard closes. The cost is that an operator cannot
// express "deny everything" on a CLI worker distinctly from the default — a
// pre-existing modelling limitation of the column, and moot here because a
// CLI worker's scope reaches its child either way.
func workerAllowlistScopeSet(raw string) bool {
	s := strings.TrimSpace(raw)
	return s != "" && s != "null" && s != "[]"
}

// workerCapabilityScopeSet reports whether capability_profile_json carries an
// operator-authored profile. This column has no default backfill, so the
// contract matches the enforcement side's parseCapabilityProfileForWorker in
// cmd/mcplexer/workers_dispatcher.go exactly: empty and "null" are absent,
// everything else means the gate would be active for an API worker. Kept
// textual on purpose — a column this package could not parse is exactly the
// case the dispatcher turns into deny-everything, so it must count as a
// requested scope rather than slip through as absent.
func workerCapabilityScopeSet(raw string) bool {
	s := strings.TrimSpace(raw)
	return s != "" && s != "null"
}
