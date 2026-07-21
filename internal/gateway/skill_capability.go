package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrCapabilityDenied indicates a skill attempted to call a tool whose
// namespace is not declared in the skill manifest's MCP servers list.
// It is a sentinel value comparable with errors.Is.
var ErrCapabilityDenied = errors.New("skill capability denied")

// builtinSkillNamespaces are namespaces a skill is always allowed to call
// regardless of its manifest. mcpx__* contains search/code-execute/cache
// helpers the skill itself runs through; mesh__* is inter-agent comms.
var builtinSkillNamespaces = map[string]struct{}{
	"mcpx": {},
	"mesh": {},
}

// checkSkillAllowlist evaluates whether the named tool is permitted under
// the skill context attached to ctx. Returns nil when the call is allowed
// (no skill context, builtin namespace, or namespace in the allowlist).
// Returns a wrapped ErrCapabilityDenied when the check fails.
func checkSkillAllowlist(ctx context.Context, toolName string) error {
	allowlist := skillAllowlistFromContext(ctx)
	if allowlist == nil {
		return nil // no skill context — allow
	}

	ns, _, ok := splitNamespace(toolName)
	if !ok {
		// Un-namespaced names are builtins; defer to the regular builtin
		// check in handler_tools.go which gates non-skill direct calls.
		return nil
	}
	if _, isBuiltin := builtinSkillNamespaces[ns]; isBuiltin {
		return nil
	}
	for _, allowed := range allowlist {
		if allowed == ns {
			return nil
		}
	}
	return fmt.Errorf("%w: namespace %q not declared by skill (allowed: %v)",
		ErrCapabilityDenied, ns, allowlist)
}

// recordSkillInvocation persists a single skill->tool dispatch attempt.
// No-ops when no skill context is active or the store is nil. Failures are
// logged but never propagated — audit/skill logs must not break dispatch.
func (h *handler) recordSkillInvocation(
	ctx context.Context, toolName string, allowed bool,
) {
	skillID := skillIDFromContext(ctx)
	if skillID == "" || h.store == nil {
		return
	}
	ns, _, _ := splitNamespace(toolName)
	inv := &store.SkillInvocation{
		SkillName: skillID,
		ToolName:  toolName,
		Namespace: ns,
		Allowed:   allowed,
	}
	if err := h.store.InsertSkillInvocation(ctx, inv); err != nil {
		slog.Warn("record skill invocation failed",
			"skill", skillID, "tool", toolName, "error", err)
	}
}
