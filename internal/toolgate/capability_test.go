package toolgate

import "testing"

// fakeWriteClass mirrors the dispatcher's writeclass heuristic for tests so
// the gate's read-only branch can be exercised without importing writeclass.
func fakeWriteClass(name string) bool {
	switch name {
	case "github__create_issue", "memory__save", "task__create",
		"task__update", "linear__update_status":
		return true
	}
	return false
}

func TestCapabilityProfileNilAllowsEverything(t *testing.T) {
	var p *CapabilityProfile
	for _, name := range []string{
		"github__create_issue", "mcplexer__create_worker",
		"memory__save", "mesh__send", "secret__list_refs",
	} {
		if ok, reason := p.Allows(name, fakeWriteClass(name)); !ok {
			t.Errorf("nil profile denied %q: %s", name, reason)
		}
	}
}

func TestCapabilityPresetGate(t *testing.T) {
	cases := []struct {
		preset string
		tool   string
		want   bool
	}{
		// full: everything except admin.
		{"full", "mcpx__execute_code", true},
		{"full", "mcpx__call_tool", true},
		{"full", "github__create_issue", true},
		{"full", "memory__save", true},
		{"full", "mesh__send", true},
		{"full", "secret__list_refs", true},
		{"full", "mcpx__delegate_worker", true},
		{"full", "mcpx__delegate_batch", true},
		{"full", "task__offer", true},
		{"full", "mcplexer__create_worker", false}, // admin always denied
		{"full", "mcpx__provision_mcp", false},     // admin mcpx always denied

		// coder: code/search + task/memory write; no mesh/secret/subdeleg.
		{"coder", "mcpx__execute_code", true},
		{"coder", "mcpx__call_tool", true},
		{"coder", "task__create", true},
		{"coder", "memory__save", true},
		{"coder", "github__create_issue", true}, // downstream not narrowed by namespace_allow
		{"coder", "mesh__send", false},
		{"coder", "secret__list_refs", false},
		{"coder", "mcpx__delegate_worker", false}, // subdelegation denied
		{"coder", "mcpx__delegate_batch", false},  // batch subdelegation also denied
		{"coder", "mcpx__invoke_model", false},
		{"coder", "task__offer", false},
		{"coder", "task__assign_remote", false},
		{"coder", "task__publish_home", false},
		{"coder", "mcplexer__create_worker", false},

		// researcher: read-only; writes denied across namespaces.
		{"researcher", "mcpx__execute_code", true},
		{"researcher", "mcpx__call_tool", true},
		{"researcher", "memory__recall", true},
		{"researcher", "task__list", true},
		{"researcher", "github__create_issue", false}, // write-class blanket deny
		{"researcher", "memory__save", false},
		{"researcher", "task__create", false},
		{"researcher", "mcpx__delegate_worker", false},
		{"researcher", "secret__list_refs", false},

		// minimal: mcpx only.
		{"minimal", "mcpx__execute_code", true},
		{"minimal", "mcpx__search_tools", true},
		{"minimal", "mcpx__call_tool", true},
		{"minimal", "mcpx__retrieve", true},
		{"minimal", "github__list_issues", false},
		{"minimal", "task__list", false},
		{"minimal", "memory__recall", false},
		{"minimal", "mesh__receive", false},
		{"minimal", "secret__list_refs", false},
		{"minimal", "skill__search", false},
		{"minimal", "email__send", false},
	}
	for _, tc := range cases {
		p, ok := ResolvePreset(tc.preset)
		if !ok || p == nil {
			t.Fatalf("ResolvePreset(%q) failed", tc.preset)
		}
		got, reason := p.Allows(tc.tool, fakeWriteClass(tc.tool))
		if got != tc.want {
			t.Errorf("preset %q tool %q: Allows=%v want %v (reason %q)",
				tc.preset, tc.tool, got, tc.want, reason)
		}
	}
}

func TestCapabilityMcpxAlwaysAllowed(t *testing.T) {
	// Even an empty-but-non-nil NamespaceAllow (deny-everything) must let
	// the irreducible mcpx entrypoint through, or the worker bricks.
	p := &CapabilityProfile{NamespaceAllow: []string{}}
	for _, name := range []string{"mcpx__execute_code", "mcpx__search_tools", "mcpx__call_tool", "mcpx__retrieve"} {
		if ok, reason := p.Allows(name, false); !ok {
			t.Errorf("deny-everything profile bricked mcpx %q: %s", name, reason)
		}
	}
	if ok, _ := p.Allows("github__list_issues", false); ok {
		t.Error("deny-everything profile allowed a downstream tool")
	}
}

func TestCapabilityProfileNeverGrantsAdmin(t *testing.T) {
	// A profile that tries to widen to admin must still be denied at the
	// runtime gate (defense-in-depth with create-time validation).
	p := &CapabilityProfile{
		NamespaceAllow: nil, // allow-all namespaces
		ToolAllow:      []string{"*"},
		Features:       CapabilityFeatures{MayUseAdmin: boolPtr(true)},
	}
	for _, name := range []string{
		"mcplexer__create_worker", "mcpx__provision_mcp",
		"task__rebind_peer",
	} {
		if ok, _ := p.Allows(name, false); ok {
			t.Errorf("profile granted admin tool %q", name)
		}
	}
}

func TestCapabilityNamespaceAllowDefaultDeny(t *testing.T) {
	// Non-nil NamespaceAllow pivots to default-DENY: only listed
	// namespaces (plus mcpx) are reachable.
	p := &CapabilityProfile{NamespaceAllow: []string{"github", "task"}}
	allow := []string{"github__list_issues", "task__create", "mcpx__execute_code"}
	deny := []string{"linear__list", "memory__recall", "mesh__send"}
	for _, n := range allow {
		if ok, reason := p.Allows(n, false); !ok {
			t.Errorf("namespace_allow denied %q: %s", n, reason)
		}
	}
	for _, n := range deny {
		if ok, _ := p.Allows(n, false); ok {
			t.Errorf("namespace_allow leaked %q", n)
		}
	}
}

func TestCapabilityToolDenyWins(t *testing.T) {
	p := &CapabilityProfile{ToolDeny: []string{"github__*"}}
	if ok, _ := p.Allows("github__list_issues", false); ok {
		t.Error("tool_deny glob did not block github__list_issues")
	}
	if ok, reason := p.Allows("linear__list", false); !ok {
		t.Errorf("tool_deny blocked unrelated tool: %s", reason)
	}
}

func TestCapabilityToolAllowNarrows(t *testing.T) {
	p := &CapabilityProfile{ToolAllow: []string{"github__list_*"}}
	if ok, reason := p.Allows("github__list_issues", false); !ok {
		t.Errorf("tool_allow blocked a matching tool: %s", reason)
	}
	if ok, _ := p.Allows("github__create_issue", false); ok {
		t.Error("tool_allow leaked a non-matching tool")
	}
}

func TestCapabilityFeatureDenyGlobs(t *testing.T) {
	none := CapabilityFeatures{}
	tools, namespaces := none.FeatureDenyGlobs()
	if len(tools) != 0 || len(namespaces) != 0 {
		t.Errorf("nil features produced denies: tools=%v ns=%v", tools, namespaces)
	}
	noWrite := CapabilityFeatures{
		MayWriteMemory: boolPtr(false),
		MayUseMesh:     boolPtr(false),
	}
	tools, namespaces = noWrite.FeatureDenyGlobs()
	if !contains(tools, "memory__save") {
		t.Errorf("may_write_memory=false did not deny memory__save: %v", tools)
	}
	if !contains(namespaces, "mesh") {
		t.Errorf("may_use_mesh=false did not deny mesh namespace: %v", namespaces)
	}
}

func TestCapabilityMergeOverrideWidensNamespace(t *testing.T) {
	// A coder profile with a namespace_allow override is the intended way
	// to widen downstream access (features only subtract).
	base := Coder()
	override := &CapabilityProfile{NamespaceAllow: []string{"github"}}
	merged := Merge(base, override)
	if ok, reason := merged.Allows("github__list_issues", false); !ok {
		t.Errorf("merged namespace_allow denied github: %s", reason)
	}
	// task is no longer in the allow list, so it is now denied.
	if ok, _ := merged.Allows("task__create", true); ok {
		t.Error("merged namespace_allow leaked task after narrowing to github")
	}
	// mcpx still survives.
	if ok, _ := merged.Allows("mcpx__execute_code", false); !ok {
		t.Error("merged profile bricked mcpx")
	}
}

func TestResolvePresetUnknown(t *testing.T) {
	if _, ok := ResolvePreset("bogus"); ok {
		t.Error("unknown preset resolved ok")
	}
	if p, ok := ResolvePreset(""); !ok || p != nil {
		t.Errorf("empty preset should resolve to (nil,true), got (%v,%v)", p, ok)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
