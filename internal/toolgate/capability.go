package toolgate

import (
	"strings"
)

// CapabilityProfile scopes a delegate worker's reachable tool surface +
// mcplexer features to its trust level. It lives in toolgate (the
// import-cycle-free home of AllowlistPatternGrantsAdmin) so create-time
// validation and runtime enforcement share one source of truth.
//
// A nil *CapabilityProfile means "no profile" => allow-all (today's
// behavior). A non-nil profile is DEFAULT-DENY for any narrowing field it
// sets: a non-nil NamespaceAllow only admits the namespaces it lists.
type CapabilityProfile struct {
	// Preset is the name this profile derives from ("full"|"coder"|
	// "researcher"|"minimal"|"" for ad-hoc). Display/audit only.
	Preset string `json:"preset,omitempty"`

	// NamespaceAllow: nil => allow all namespaces (subject to
	// NamespaceDeny + admin guard). Non-nil (incl. empty) => default-DENY:
	// only listed namespaces are reachable. Names are the builtin/
	// downstream namespace segment ("github", "task", "memory", "mesh",
	// "secret", "skill", "email").
	NamespaceAllow []string `json:"namespace_allow,omitempty"`
	NamespaceDeny  []string `json:"namespace_deny,omitempty"`

	// ToolAllow/ToolDeny: per-tool glob gating layered ON TOP of the
	// namespace decision, using path.Match semantics. nil ToolAllow => no
	// per-tool narrowing. Non-nil => a tool must also match one ToolAllow
	// glob. ToolDeny always wins (checked first).
	ToolAllow []string `json:"tool_allow,omitempty"`
	ToolDeny  []string `json:"tool_deny,omitempty"`

	// Features are pointer-typed bool flags so "unset" (inherit preset
	// default) is distinguishable from "explicitly false". Resolution
	// flattens them to concrete bools before persistence.
	Features CapabilityFeatures `json:"features,omitempty"`
}

// CapabilityFeatures are feature flags that compile to tool-glob / namespace
// DENY entries. They can only SUBTRACT, never widen a NamespaceAllow.
type CapabilityFeatures struct {
	MayWriteMemory         *bool `json:"may_write_memory,omitempty"`
	MayCreateSubdelegation *bool `json:"may_create_subdelegation,omitempty"`
	MayOfferTasks          *bool `json:"may_offer_tasks,omitempty"`
	MayWriteTasks          *bool `json:"may_write_tasks,omitempty"`
	MayUseMesh             *bool `json:"may_use_mesh,omitempty"`
	MayUseSecrets          *bool `json:"may_use_secrets,omitempty"`
	// MayUseAdmin is ALWAYS forced false for delegates. Accepting it set
	// to true is rejected at create time.
	MayUseAdmin *bool `json:"may_use_admin,omitempty"`
}

// mcpxNamespace is the mcpx builtin namespace segment. The Minimal preset
// lists it in NamespaceAllow so that, in addition to the two always-allow
// entrypoint tools (mcpxEntrypointTools in capability_gate.go), the other
// non-admin, non-feature-denied mcpx builtins remain reachable under the
// otherwise-deny-everything minimal floor. Note: only mcpx__execute_code and
// mcpx__search_tools get the unconditional Allows() bypass; everything else
// in the mcpx namespace is subject to the normal feature / namespace gates.
const mcpxNamespace = "mcpx"

func boolPtr(b bool) *bool { return &b }

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Full returns the high-trust preset: today's default surface, no extra
// narrowing beyond the always-on admin hard-deny.
func Full() *CapabilityProfile {
	return &CapabilityProfile{
		Preset: "full",
		Features: CapabilityFeatures{
			MayWriteMemory:         boolPtr(true),
			MayCreateSubdelegation: boolPtr(true),
			MayOfferTasks:          boolPtr(true),
			MayWriteTasks:          boolPtr(true),
			MayUseMesh:             boolPtr(true),
			MayUseSecrets:          boolPtr(true),
		},
	}
}

// Coder returns the narrow-coder preset: code-mode + discovery + task/memory
// writes, but no subdelegation, mesh, or secrets.
func Coder() *CapabilityProfile {
	return &CapabilityProfile{
		Preset: "coder",
		Features: CapabilityFeatures{
			MayWriteMemory:         boolPtr(true),
			MayWriteTasks:          boolPtr(true),
			MayCreateSubdelegation: boolPtr(false),
			MayOfferTasks:          boolPtr(false),
			MayUseMesh:             boolPtr(false),
			MayUseSecrets:          boolPtr(false),
		},
	}
}

// Researcher returns the read-only preset: discovery + read tools across
// namespaces, NO state mutation anywhere.
func Researcher() *CapabilityProfile {
	return &CapabilityProfile{
		Preset: "researcher",
		Features: CapabilityFeatures{
			MayWriteMemory:         boolPtr(false),
			MayWriteTasks:          boolPtr(false),
			MayOfferTasks:          boolPtr(false),
			MayCreateSubdelegation: boolPtr(false),
			MayUseMesh:             boolPtr(false),
			MayUseSecrets:          boolPtr(false),
		},
	}
}

// Minimal returns the bare-slim preset: ONLY the mcpx namespace
// (search_tools + execute_code + pure compute), nothing else.
func Minimal() *CapabilityProfile {
	return &CapabilityProfile{
		Preset:         "minimal",
		NamespaceAllow: []string{mcpxNamespace},
		Features: CapabilityFeatures{
			MayWriteMemory:         boolPtr(false),
			MayWriteTasks:          boolPtr(false),
			MayOfferTasks:          boolPtr(false),
			MayCreateSubdelegation: boolPtr(false),
			MayUseMesh:             boolPtr(false),
			MayUseSecrets:          boolPtr(false),
		},
	}
}

// ResolvePreset returns the named preset, or (nil, false) for an unknown or
// empty name. "" resolves to (nil, true): an ad-hoc custom profile with no
// preset floor (the override carries all gating).
func ResolvePreset(name string) (*CapabilityProfile, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return nil, true
	case "full":
		return Full(), true
	case "coder":
		return Coder(), true
	case "researcher":
		return Researcher(), true
	case "minimal":
		return Minimal(), true
	default:
		return nil, false
	}
}

// Merge layers override ON TOP of base, per-field: a non-nil/non-empty
// override field wins; nil/zero override fields inherit base. Features merge
// per-pointer (a non-nil override pointer wins). Returns a new profile;
// inputs are not mutated. Either argument may be nil.
func Merge(base, override *CapabilityProfile) *CapabilityProfile {
	if base == nil && override == nil {
		return nil
	}
	out := &CapabilityProfile{}
	if base != nil {
		*out = cloneProfile(base)
	}
	if override == nil {
		return out
	}
	ov := cloneProfile(override)
	if ov.Preset != "" {
		out.Preset = ov.Preset
	}
	if ov.NamespaceAllow != nil {
		// REPLACES (does not union) base.NamespaceAllow: an override
		// NamespaceAllow is the new authoritative allow-list. mcpx survival
		// across this replacement does NOT depend on "mcpx" being present in
		// the resulting list — the two entrypoint tools (mcpx__execute_code /
		// mcpx__search_tools) are always-allowed by Allows()'s hardcoded
		// mcpxEntrypointTools bypass, which runs before the NamespaceAllow
		// gate. So a Merge that narrows to e.g. ["github"] still leaves
		// search/execute reachable by construction; other mcpx tools become
		// reachable only if "mcpx" is also listed.
		out.NamespaceAllow = ov.NamespaceAllow
	}
	if len(ov.NamespaceDeny) > 0 {
		out.NamespaceDeny = unionStrings(out.NamespaceDeny, ov.NamespaceDeny)
	}
	if ov.ToolAllow != nil {
		out.ToolAllow = ov.ToolAllow
	}
	if len(ov.ToolDeny) > 0 {
		out.ToolDeny = unionStrings(out.ToolDeny, ov.ToolDeny)
	}
	out.Features = mergeFeatures(out.Features, ov.Features)
	return out
}

func mergeFeatures(base, ov CapabilityFeatures) CapabilityFeatures {
	pick := func(b, o *bool) *bool {
		if o != nil {
			return o
		}
		return b
	}
	return CapabilityFeatures{
		MayWriteMemory:         pick(base.MayWriteMemory, ov.MayWriteMemory),
		MayCreateSubdelegation: pick(base.MayCreateSubdelegation, ov.MayCreateSubdelegation),
		MayOfferTasks:          pick(base.MayOfferTasks, ov.MayOfferTasks),
		MayWriteTasks:          pick(base.MayWriteTasks, ov.MayWriteTasks),
		MayUseMesh:             pick(base.MayUseMesh, ov.MayUseMesh),
		MayUseSecrets:          pick(base.MayUseSecrets, ov.MayUseSecrets),
		MayUseAdmin:            pick(base.MayUseAdmin, ov.MayUseAdmin),
	}
}

func cloneProfile(p *CapabilityProfile) CapabilityProfile {
	out := CapabilityProfile{Preset: p.Preset, Features: p.Features}
	if p.NamespaceAllow != nil {
		out.NamespaceAllow = append([]string(nil), p.NamespaceAllow...)
	}
	if p.NamespaceDeny != nil {
		out.NamespaceDeny = append([]string(nil), p.NamespaceDeny...)
	}
	if p.ToolAllow != nil {
		out.ToolAllow = append([]string(nil), p.ToolAllow...)
	}
	if p.ToolDeny != nil {
		out.ToolDeny = append([]string(nil), p.ToolDeny...)
	}
	return out
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string(nil), a...), b...) {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
