package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/toolgate"
)

func validateDelegationAllowlistJSON(raw string) error {
	if err := validateAllowlistJSON(raw); err != nil {
		return err
	}
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(s), &names); err != nil {
		return fmt.Errorf("tool_allowlist_json must be a JSON array of strings: %w", err)
	}
	for i, pattern := range names {
		if toolgate.AllowlistPatternGrantsAdmin(pattern) {
			return fmt.Errorf(
				"tool_allowlist_json[%d] %q grants admin-only tools; delegated workers cannot include admin tools",
				i, pattern,
			)
		}
	}
	return nil
}

// resolveDelegationCapabilityProfile resolves capability_preset +
// capability_profile into a concrete validated profile, marshals it, and
// stamps it onto in.capabilityProfileJSON. When neither is supplied it
// leaves the JSON empty (no profile => allow-all => today's behavior).
func resolveDelegationCapabilityProfile(in *DelegationInput) error {
	preset := strings.TrimSpace(in.CapabilityPreset)
	if preset == "" && in.CapabilityProfile == nil {
		return nil // back-compat: no scoping requested.
	}
	base, ok := toolgate.ResolvePreset(preset)
	if !ok {
		return fmt.Errorf("capability_preset %q is not one of full|coder|researcher|minimal", preset)
	}
	resolved := toolgate.Merge(base, in.CapabilityProfile)
	if resolved == nil {
		return nil
	}
	if preset != "" {
		resolved.Preset = strings.ToLower(preset)
	}
	if err := validateCapabilityProfile(resolved); err != nil {
		return err
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshal capability profile: %w", err)
	}
	in.capabilityProfileJSON = string(raw)
	return nil
}

// delegationCapabilityPresetLabel returns the display label for the
// Delegations UI: the explicit preset, else "custom" when an ad-hoc profile
// was supplied, else "" when no scoping was requested.
func delegationCapabilityPresetLabel(in DelegationInput) string {
	if preset := strings.TrimSpace(in.CapabilityPreset); preset != "" {
		return strings.ToLower(preset)
	}
	if in.CapabilityProfile != nil {
		return "custom"
	}
	return ""
}

// validateCapabilityProfileJSON parses the persisted column form and runs
// validateCapabilityProfile over it. Empty / "null" is allowed (no profile).
// Used by the worker create/update admin paths so the dashboard worker-edit
// form and template install cannot persist an admin-reopening profile.
func validateCapabilityProfileJSON(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return nil
	}
	var p toolgate.CapabilityProfile
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return fmt.Errorf("capability_profile_json must be a JSON object: %w", err)
	}
	return validateCapabilityProfile(&p)
}

// validateCapabilityProfile rejects any profile that could re-open admin to a
// delegate. Delegates bypass the admin CWD gate for in-process calls, so the
// guard must hold at create time (defense-in-depth with the runtime
// IsAdminTool always-deny in toolgate.Allows). Rejects:
//   - features.may_use_admin = true
//   - any tool_allow glob that AllowlistPatternGrantsAdmin
//   - namespace_allow containing "mcplexer" (the legacy admin namespace)
func validateCapabilityProfile(p *toolgate.CapabilityProfile) error {
	if p == nil {
		return nil
	}
	if p.Features.MayUseAdmin != nil && *p.Features.MayUseAdmin {
		return fmt.Errorf("capability_profile.features.may_use_admin cannot be true; delegated workers can never get admin")
	}
	for i, glob := range p.ToolAllow {
		if toolgate.AllowlistPatternGrantsAdmin(glob) {
			return fmt.Errorf(
				"capability_profile.tool_allow[%d] %q grants admin-only tools; delegated workers cannot include admin tools",
				i, glob,
			)
		}
	}
	for i, ns := range p.NamespaceAllow {
		if strings.EqualFold(strings.TrimSpace(ns), "mcplexer") {
			return fmt.Errorf(
				"capability_profile.namespace_allow[%d] %q is the admin namespace; delegated workers cannot reach admin tools",
				i, ns,
			)
		}
	}
	return nil
}
