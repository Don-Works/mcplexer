package gateway

import (
	"context"
	"encoding/json"

	"github.com/don-works/mcplexer/internal/skills"
)

// SkillRunResult is the value returned by Server.ExecuteSkill — a thin
// wrapper around the raw MCP CallToolResult JSON produced by the code-mode
// dispatcher. Errors at the RPC envelope level (e.g. capability denial
// before a single tool fired) are surfaced as RPCError on the response.
type SkillRunResult struct {
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error,omitempty"`
}

// ExecuteSkill loads the supplied manifest, attaches a skill context to ctx
// (skill_id + namespace allowlist), and dispatches the skill body through
// the standard code-mode flow. The body must be JavaScript (the only
// runtime supported in v1 — see ADR 0004).
//
// id is the caller-chosen skill identifier (typically the manifest name)
// recorded in audit/skill_invocations rows. body is the skill's
// JavaScript entry point as a string. manifest is the parsed manifest
// whose Capabilities.MCPServers list defines the namespace allowlist.
func (s *Server) ExecuteSkill(
	ctx context.Context,
	id string,
	manifest *skills.Manifest,
	body string,
) SkillRunResult {
	if id == "" {
		return SkillRunResult{Error: &RPCError{
			Code: CodeInvalidParams, Message: "skill id required",
		}}
	}
	if manifest == nil {
		return SkillRunResult{Error: &RPCError{
			Code: CodeInvalidParams, Message: "skill manifest required",
		}}
	}
	if body == "" {
		return SkillRunResult{Error: &RPCError{
			Code: CodeInvalidParams, Message: "skill body required",
		}}
	}

	allow := allowlistFromManifest(manifest)
	skillCtx := withSkillID(withSkillAllowlist(ctx, allow), id)

	result, rpcErr := s.handler.handleCodeExecute(skillCtx, body)
	if rpcErr != nil {
		return SkillRunResult{Error: rpcErr}
	}
	return SkillRunResult{Result: result}
}

// allowlistFromManifest extracts namespace names from the skill manifest's
// declared MCP servers. Always non-nil — an empty slice means "no
// downstream namespaces allowed" (only mcpx__/mesh__ builtins).
func allowlistFromManifest(m *skills.Manifest) []string {
	out := make([]string, 0, len(m.Capabilities.MCPServers))
	for _, srv := range m.Capabilities.MCPServers {
		if srv.Name != "" {
			out = append(out, srv.Name)
		}
	}
	return out
}
