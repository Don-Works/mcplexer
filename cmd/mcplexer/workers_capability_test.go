package main

import "testing"

func TestParseCapabilityProfileForWorkerEmptyIsNil(t *testing.T) {
	for _, raw := range []string{"", "   ", "null"} {
		if p := parseCapabilityProfileForWorker("wkr-1", raw); p != nil {
			t.Errorf("raw %q produced non-nil profile (should be allow-all)", raw)
		}
	}
}

func TestParseCapabilityProfileForWorkerValid(t *testing.T) {
	raw := `{"preset":"coder","namespace_allow":["github","task","mcpx"]}`
	p := parseCapabilityProfileForWorker("wkr-1", raw)
	if p == nil {
		t.Fatal("valid profile parsed to nil")
	}
	if p.Preset != "coder" {
		t.Errorf("preset = %q, want coder", p.Preset)
	}
	if ok, _ := p.Allows("github__list_issues", false); !ok {
		t.Error("parsed profile denied an allowed namespace")
	}
	if ok, _ := p.Allows("linear__list", false); ok {
		t.Error("parsed profile allowed a denied namespace")
	}
}

func TestParseCapabilityProfileForWorkerCorruptFailsClosed(t *testing.T) {
	// A corrupt column must deny everything (except mcpx), not silently
	// widen to allow-all.
	p := parseCapabilityProfileForWorker("wkr-1", `{not valid json`)
	if p == nil {
		t.Fatal("corrupt profile parsed to nil (would silently allow-all)")
	}
	if ok, _ := p.Allows("github__list_issues", false); ok {
		t.Error("corrupt profile allowed a downstream tool")
	}
	if ok, _ := p.Allows("mcpx__execute_code", false); !ok {
		t.Error("corrupt fail-closed profile bricked mcpx entrypoint")
	}
}

func TestDenyEverythingCapabilityProfile(t *testing.T) {
	p := denyEverythingCapabilityProfile()
	if p == nil {
		t.Fatal("deny-everything profile must be non-nil")
	}
	// HIGH-2: a bare NamespaceAllow:[] is NOT deny-everything — nil feature
	// flags default ALLOWED, so the mcpx bypass would still permit
	// delegate_worker / invoke_model for an unidentifiable worker. The
	// fail-closed profile must explicitly deny those.
	for _, name := range []string{
		"mcpx__delegate_worker", "mcpx__invoke_model",
		"github__list_issues", "task__create", "memory__save", "mesh__send",
	} {
		if ok, _ := p.Allows(name, false); ok {
			t.Errorf("fail-closed profile allowed %q (must deny everything but the entrypoints)", name)
		}
	}
	// The two irreducible entrypoints must survive or the worker bricks.
	for _, name := range []string{"mcpx__execute_code", "mcpx__search_tools"} {
		if ok, reason := p.Allows(name, false); !ok {
			t.Errorf("fail-closed profile bricked entrypoint %q: %s", name, reason)
		}
	}
}
