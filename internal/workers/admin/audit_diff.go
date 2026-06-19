// Package admin — audit_diff.go owns the per-field diff helpers used
// by emitAuditUpdate. Split off from audit.go to honour the 300-line
// budget.
//
// The result is a {field_name: {old, new}} map. Only the fields the
// UpdateInput actually mutated (non-nil pointer fields) appear, so the
// audit row never carries unchanged baseline data. Long opaque fields
// (prompt_template, parameters_json, tool_allowlist_json,
// output_channels_json) render as fingerprint pairs; everything else is
// the literal value.
package admin

import "github.com/don-works/mcplexer/internal/store"

// buildUpdateDiff walks UpdateInput and returns a {field: {old, new}}
// map containing only the fields actually mutated.
func buildUpdateDiff(old *store.Worker, in UpdateInput) map[string]any {
	d := map[string]any{}
	addShortDiffs(d, old, in)
	addLongDiffs(d, old, in)
	addCapDiffs(d, old, in)
	if in.SkillRefs != nil {
		d["skill_refs"] = map[string]any{
			"old_count": len(old.SkillRefs),
			"new_count": len(*in.SkillRefs),
		}
	}
	if in.WorkspaceAccess != nil {
		d["workspace_access"] = map[string]any{
			"old_count": len(old.WorkspaceAccess),
			"new_count": len(*in.WorkspaceAccess),
		}
	}
	if in.AutoPausedReason != nil {
		d["auto_paused_reason"] = map[string]any{
			"old": old.AutoPausedReason, "new": *in.AutoPausedReason,
		}
	}
	return d
}

// addShortDiffs handles the short-string + bool fields where the literal
// value is small enough to land in the audit row verbatim.
func addShortDiffs(d map[string]any, old *store.Worker, in UpdateInput) {
	if in.Name != nil {
		d["name"] = map[string]any{"old": old.Name, "new": *in.Name}
	}
	if in.Description != nil {
		d["description"] = map[string]any{"old": old.Description, "new": *in.Description}
	}
	if in.ModelProvider != nil {
		d["model_provider"] = map[string]any{"old": old.ModelProvider, "new": *in.ModelProvider}
	}
	if in.ModelID != nil {
		d["model_id"] = map[string]any{"old": old.ModelID, "new": *in.ModelID}
	}
	if in.SecretScopeID != nil {
		d["secret_scope_id"] = map[string]any{"old": old.SecretScopeID, "new": *in.SecretScopeID}
	}
	if in.ScheduleSpec != nil {
		d["schedule_spec"] = map[string]any{"old": old.ScheduleSpec, "new": *in.ScheduleSpec}
	}
	if in.ExecMode != nil {
		d["exec_mode"] = map[string]any{"old": old.ExecMode, "new": *in.ExecMode}
	}
	if in.ConcurrencyPolicy != nil {
		d["concurrency_policy"] = map[string]any{"old": old.ConcurrencyPolicy, "new": *in.ConcurrencyPolicy}
	}
	if in.MemoryScopeID != nil {
		d["memory_scope_id"] = map[string]any{"old": old.MemoryScopeID, "new": *in.MemoryScopeID}
	}
	if in.Enabled != nil {
		d["enabled"] = map[string]any{"old": old.Enabled, "new": *in.Enabled}
	}
	if in.WorkspaceID != nil {
		d["workspace_id"] = map[string]any{"old": old.WorkspaceID, "new": *in.WorkspaceID}
	}
	if in.SkillName != nil {
		d["skill_name"] = map[string]any{"old": old.SkillName, "new": *in.SkillName}
	}
	if in.SkillVersion != nil {
		d["skill_version"] = map[string]any{"old": old.SkillVersion, "new": *in.SkillVersion}
	}
}

// addLongDiffs handles the long opaque fields, rendered as fingerprint
// old/new pairs so the audit row never carries the body.
//
// model_endpoint_url is fingerprinted (not bodied) because openai_compat
// workers can carry tokens in the URL path or query string (e.g.
// "https://api.example.com/v1?key=…"). The verbatim form would land in
// audit_records.params_redacted and bypass the secret-redaction layer
// that protects scope-managed credentials.
func addLongDiffs(d map[string]any, old *store.Worker, in UpdateInput) {
	if in.ModelEndpointURL != nil {
		d["model_endpoint_url"] = map[string]any{
			"old": fingerprint(old.ModelEndpointURL),
			"new": fingerprint(*in.ModelEndpointURL),
		}
	}
	if in.PromptTemplate != nil {
		d["prompt_template"] = map[string]any{
			"old": fingerprint(old.PromptTemplate),
			"new": fingerprint(*in.PromptTemplate),
		}
	}
	if in.ParametersJSON != nil {
		d["parameters_json"] = map[string]any{
			"old": fingerprint(old.ParametersJSON),
			"new": fingerprint(*in.ParametersJSON),
		}
	}
	if in.ToolAllowlistJSON != nil {
		d["tool_allowlist_json"] = map[string]any{
			"old": fingerprint(old.ToolAllowlistJSON),
			"new": fingerprint(*in.ToolAllowlistJSON),
		}
	}
	if in.CapabilityProfileJSON != nil {
		d["capability_profile_json"] = map[string]any{
			"old": fingerprint(old.CapabilityProfileJSON),
			"new": fingerprint(*in.CapabilityProfileJSON),
		}
	}
	if in.OutputChannelsJSON != nil {
		d["output_channels_json"] = map[string]any{
			"old": fingerprint(old.OutputChannelsJSON),
			"new": fingerprint(*in.OutputChannelsJSON),
		}
	}
}

// addCapDiffs handles the per-worker safety cap diffs.
func addCapDiffs(d map[string]any, old *store.Worker, in UpdateInput) {
	if in.MaxInputTokens != nil {
		d["max_input_tokens"] = map[string]any{"old": old.MaxInputTokens, "new": *in.MaxInputTokens}
	}
	if in.MaxOutputTokens != nil {
		d["max_output_tokens"] = map[string]any{"old": old.MaxOutputTokens, "new": *in.MaxOutputTokens}
	}
	if in.MaxToolCalls != nil {
		d["max_tool_calls"] = map[string]any{"old": old.MaxToolCalls, "new": *in.MaxToolCalls}
	}
	if in.MaxWallClockSeconds != nil {
		d["max_wall_clock_seconds"] = map[string]any{"old": old.MaxWallClockSeconds, "new": *in.MaxWallClockSeconds}
	}
	if in.MaxMonthlyCostUSD != nil {
		d["max_monthly_cost_usd"] = map[string]any{"old": old.MaxMonthlyCostUSD, "new": *in.MaxMonthlyCostUSD}
	}
	if in.MaxConsecutiveFailures != nil {
		d["max_consecutive_failures"] = map[string]any{"old": old.MaxConsecutiveFailures, "new": *in.MaxConsecutiveFailures}
	}
}
