package toolgate

import (
	"path"
	"strings"
)

// memoryReadTools is the allowlist of READ-class memory__* builtins. Under
// may_write_memory=false, ANY memory__* tool NOT in this set is denied. This
// is DRIFT-PROOF: a newly-added memory write tool is denied by default
// (absent from the read-allowlist) rather than silently leaking until someone
// remembers to add it to a hand-maintained deny list. Mirrors the registered
// memory builtins (builtin_tools_memory.go); keep in sync when adding READ
// tools — the gate fails closed for any name not listed here.
//
// memory__request_memory is deliberately EXCLUDED (treated as write/deny): it
// pulls a peer's memory entry INTO the local store — a mutation, not a read.
var memoryReadTools = map[string]bool{
	"memory__recall":               true,
	"memory__recall_about":         true,
	"memory__get":                  true,
	"memory__list":                 true,
	"memory__list_entities":        true,
	"memory__related_entities":     true,
	"memory__spreading_activation": true,
	"memory__co_recalled":          true,
	"memory__suggestions":          true,
}

// taskReadTools is the allowlist of READ-class task__* builtins. Under
// may_write_tasks=false, ANY task__* tool NOT in this set is denied
// (drift-proof, same contract as memoryReadTools). Mirrors the registered
// task builtins (builtin_tools_tasks.go). task__heartbeat is a READ-adjacent
// lease keepalive (no row mutation beyond the lease the caller already owns)
// and is allowed so a read-only worker holding a claim doesn't lose it.
var taskReadTools = map[string]bool{
	"task__get":              true,
	"task__list":             true,
	"task__list_milestones":  true,
	"task__list_offers":      true,
	"task__list_attachments": true,
	"task__get_attachment":   true,
	"task__recent_activity":  true,
	"task__heartbeat":        true,
}

// deniesTool reports whether the feature flags deny toolName, with the
// REASON. This is the single canonical feature->deny mapping consulted by
// Allows. Features can only SUBTRACT (every branch is a denial). It runs
// BEFORE the mcpx always-allow in Allows, so a feature-denied mcpx tool
// (e.g. mcpx__delegate_worker under may_create_subdelegation=false) cannot
// ride the entrypoint bypass.
//
// For memory/tasks the deny is derived from the READ allowlist, not a hand
// list of write names: deny every namespaced tool that is NOT a known read
// tool. Drift-proof — a new write tool is denied by default.
func (f CapabilityFeatures) deniesTool(toolName string) (bool, string) {
	if !derefBool(f.MayCreateSubdelegation, true) {
		if toolName == "mcpx__delegate_worker" || toolName == "mcpx__delegate_batch" ||
			toolName == "mcpx__invoke_model" || toolName == "mcpx__extend_delegation_budget" {
			return true, "tool denied by capability feature flag (may_create_subdelegation=false)"
		}
	}
	if !derefBool(f.MayOfferTasks, true) {
		if toolName == "task__offer" || toolName == "task__assign_remote" || toolName == "task__publish_home" {
			return true, "tool denied by capability feature flag (may_offer_tasks=false)"
		}
	}
	if !derefBool(f.MayWriteMemory, true) && namespaceSegment(toolName) == "memory" {
		if !memoryReadTools[toolName] {
			return true, "memory write denied by capability feature flag (may_write_memory=false)"
		}
	}
	if !derefBool(f.MayWriteTasks, true) && namespaceSegment(toolName) == "task" {
		if !taskReadTools[toolName] {
			return true, "task write denied by capability feature flag (may_write_tasks=false)"
		}
	}
	return false, ""
}

// FeatureDenyGlobs compiles the feature flags into a representative set of
// tool-glob / namespace DENY entries. Retained for callers that want a static
// preview of the deny surface (and back-compat tests); the AUTHORITATIVE
// per-tool decision is deniesTool, which Allows consults. The memory/task
// tool lists here are illustrative literals — the real gate is the read-
// allowlist derivation in deniesTool, so this list need not be exhaustive.
//
// Returns two lists: tool-name denials (literal or glob) and namespace
// denials (segment names).
func (f CapabilityFeatures) FeatureDenyGlobs() (tools []string, namespaces []string) {
	if !derefBool(f.MayWriteMemory, true) {
		tools = append(tools,
			"memory__save", "memory__update", "memory__forget",
			"memory__link", "memory__offer")
	}
	if !derefBool(f.MayCreateSubdelegation, true) {
		tools = append(tools, "mcpx__delegate_worker", "mcpx__delegate_batch", "mcpx__invoke_model", "mcpx__extend_delegation_budget")
	}
	if !derefBool(f.MayOfferTasks, true) {
		tools = append(tools, "task__offer", "task__assign_remote", "task__publish_home")
	}
	if !derefBool(f.MayWriteTasks, true) {
		tools = append(tools,
			"task__create", "task__update", "task__claim", "task__append_note")
	}
	if !derefBool(f.MayUseMesh, true) {
		namespaces = append(namespaces, "mesh")
	}
	if !derefBool(f.MayUseSecrets, true) {
		namespaces = append(namespaces, "secret")
	}
	return tools, namespaces
}

// isReadOnly reports whether the profile marks the worker as read-only:
// every may_write_* family flag explicitly false. Drives the writeclass
// blanket-deny in Allows (step 9).
func (f CapabilityFeatures) isReadOnly() bool {
	return !derefBool(f.MayWriteMemory, true) &&
		!derefBool(f.MayWriteTasks, true) &&
		!derefBool(f.MayOfferTasks, true) &&
		!derefBool(f.MayCreateSubdelegation, true)
}

// mcpxEntrypointTools is the EXACT set of mcpx__ tools that ride the
// irreducible always-allow bypass — search/execute plus lossless CCR retrieval. Gating the
// bypass to these specific names (rather than the bare "mcpx" namespace
// segment) means every OTHER mcpx tool (delegate_worker, invoke_model,
// review_delegation, skill_*, …) falls through to the normal feature /
// namespace / read-only gates. Without this, a downstream server provisioned
// under the "mcpx" namespace, or any non-entrypoint mcpx builtin, would ride
// the bare-segment bypass unchecked (LOW-6). Admin mcpx tools are already
// hard-denied first via IsAdminTool; this tightens the non-admin remainder.
var mcpxEntrypointTools = map[string]bool{
	"mcpx__execute_code": true,
	"mcpx__search_tools": true,
	"mcpx__retrieve":     true,
}

// Allows reports whether toolName is reachable under this profile. isWriteClass
// is computed by the caller (the writeclass heuristic) and passed in so
// toolgate stays import-cycle-free. The second return is a human-readable
// deny reason (empty when allowed).
//
// Resolution order (default-DENY pivots are documented inline):
//  1. nil receiver => allow (back-compat).
//  2. admin tool => always DENY (delegates never get admin) — runs FIRST so
//     an admin mcpx__ tool (e.g. mcpx__provision_mcp) cannot ride the mcpx
//     entrypoint bypass.
//  3. ToolDeny glob match => DENY.
//  4. feature-derived tool deny => DENY (catches mcpx__delegate_worker /
//     mcpx__invoke_model when subdelegation is off, and the drift-proof
//     memory/task write denies, before the mcpx entrypoint bypass).
//  5. mcpx ENTRYPOINT tools (execute_code / search_tools) => allow
//     (irreducible) — bypasses ONLY the namespace_deny / namespace_allow /
//     read-only gates below, never the admin / tool-deny / feature-deny
//     checks above. NON-entrypoint mcpx tools fall through to the gates.
//  6. NamespaceDeny / feature-derived namespace deny => DENY.
//  7. NamespaceAllow non-nil AND namespace not listed => DENY.
//  8. ToolAllow non-nil AND no glob match => DENY.
//  9. read-only profile AND write-class tool => DENY (best-effort blanket for
//     arbitrary downstream mutators; relies on the writeclass heuristic,
//     which is over-permissive on the write side — a residual gap remains for
//     an exotically-named downstream write tool with no recognizable verb).
//  10. ALLOW.
func (p *CapabilityProfile) Allows(toolName string, isWriteClass bool) (bool, string) {
	if p == nil {
		return true, ""
	}
	if IsAdminTool(toolName) {
		return false, "admin tools are never available to delegated workers"
	}
	for _, glob := range p.ToolDeny {
		if capPatternMatches(glob, toolName) {
			return false, "tool denied by capability profile tool_deny"
		}
	}
	if denied, reason := p.Features.deniesTool(toolName); denied {
		return false, reason
	}
	ns := namespaceSegment(toolName)
	// The worker surface plus CCR recovery is the irreducible entrypoint: it bypasses
	// the namespace and read-only gates so search/execute/retrieve always work, but
	// only AFTER the admin + tool-deny + feature-deny checks above have had
	// their say, and ONLY for the exact entrypoint names (not the whole mcpx
	// segment — see mcpxEntrypointTools).
	if mcpxEntrypointTools[toolName] {
		return true, ""
	}
	for _, deny := range p.NamespaceDeny {
		if strings.EqualFold(strings.TrimSpace(deny), ns) {
			return false, "namespace denied by capability profile namespace_deny"
		}
	}
	if !derefBool(p.Features.MayUseMesh, true) && ns == "mesh" {
		return false, "namespace denied by capability feature flag (may_use_mesh=false)"
	}
	if !derefBool(p.Features.MayUseSecrets, true) && ns == "secret" {
		return false, "namespace denied by capability feature flag (may_use_secrets=false)"
	}
	if p.NamespaceAllow != nil && !nsInList(ns, p.NamespaceAllow) {
		return false, "namespace not in capability profile namespace_allow"
	}
	if p.ToolAllow != nil {
		matched := false
		for _, glob := range p.ToolAllow {
			if capPatternMatches(glob, toolName) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "tool not in capability profile tool_allow"
		}
	}
	if p.Features.isReadOnly() && isWriteClass {
		return false, "write-class tool denied by read-only capability profile"
	}
	return true, ""
}

func nsInList(ns string, list []string) bool {
	for _, n := range list {
		if strings.EqualFold(strings.TrimSpace(n), ns) {
			return true
		}
	}
	return false
}

// namespaceSegment extracts the leading "namespace" from "namespace__tool".
// Mirrors splitNamespace in the gateway but lives here so toolgate is the
// single source of truth for the gate.
func namespaceSegment(name string) string {
	for i := 0; i+1 < len(name); i++ {
		if name[i] == '_' && name[i+1] == '_' {
			return name[:i]
		}
	}
	return name
}

func capPatternMatches(pattern, toolName string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == toolName {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	ok, err := path.Match(pattern, toolName)
	return err == nil && ok
}
