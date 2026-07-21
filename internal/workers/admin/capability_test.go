package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/toolgate"
)

func TestResolveDelegationCapabilityProfileNoneIsBackCompat(t *testing.T) {
	in := &DelegationInput{}
	if err := resolveDelegationCapabilityProfile(in); err != nil {
		t.Fatalf("resolve no-profile: %v", err)
	}
	if in.capabilityProfileJSON != "" {
		t.Errorf("no profile requested but JSON set: %q", in.capabilityProfileJSON)
	}
}

func TestResolveDelegationCapabilityProfilePreset(t *testing.T) {
	in := &DelegationInput{CapabilityPreset: "coder"}
	if err := resolveDelegationCapabilityProfile(in); err != nil {
		t.Fatalf("resolve coder preset: %v", err)
	}
	if in.capabilityProfileJSON == "" {
		t.Fatal("coder preset produced empty profile JSON")
	}
	var p toolgate.CapabilityProfile
	if err := json.Unmarshal([]byte(in.capabilityProfileJSON), &p); err != nil {
		t.Fatalf("unmarshal resolved profile: %v", err)
	}
	if p.Preset != "coder" {
		t.Errorf("preset label = %q, want coder", p.Preset)
	}
	// coder must block subdelegation + mesh + secret, allow code/task/memory.
	if ok, _ := p.Allows("mcpx__delegate_worker", false); ok {
		t.Error("coder profile allowed subdelegation")
	}
	if ok, _ := p.Allows("mcpx__execute_code", false); !ok {
		t.Error("coder profile blocked execute_code")
	}
}

func TestResolveDelegationCapabilityProfileUnknownPreset(t *testing.T) {
	in := &DelegationInput{CapabilityPreset: "wizard"}
	if err := resolveDelegationCapabilityProfile(in); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestResolveDelegationCapabilityProfileRejectsAdmin(t *testing.T) {
	mayAdmin := true
	cases := []struct {
		name string
		in   *DelegationInput
	}{
		{
			name: "may_use_admin",
			in: &DelegationInput{
				CapabilityProfile: &toolgate.CapabilityProfile{
					Features: toolgate.CapabilityFeatures{MayUseAdmin: &mayAdmin},
				},
			},
		},
		{
			name: "admin_tool_allow_glob",
			in: &DelegationInput{
				CapabilityProfile: &toolgate.CapabilityProfile{
					ToolAllow: []string{"mcplexer__*"},
				},
			},
		},
		{
			name: "mcplexer_namespace_allow",
			in: &DelegationInput{
				CapabilityProfile: &toolgate.CapabilityProfile{
					NamespaceAllow: []string{"mcplexer"},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := resolveDelegationCapabilityProfile(tc.in); err == nil {
				t.Error("admin-reopening profile was accepted")
			}
		})
	}
}

func TestResolveDelegationCapabilityProfileMergeOverride(t *testing.T) {
	// coder preset + namespace_allow override is the widen path for the
	// project's downstream set.
	in := &DelegationInput{
		CapabilityPreset: "coder",
		CapabilityProfile: &toolgate.CapabilityProfile{
			NamespaceAllow: []string{"github"},
		},
	}
	if err := resolveDelegationCapabilityProfile(in); err != nil {
		t.Fatalf("resolve merge: %v", err)
	}
	var p toolgate.CapabilityProfile
	if err := json.Unmarshal([]byte(in.capabilityProfileJSON), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := p.Allows("github__list_issues", false); !ok {
		t.Error("merged profile did not allow github after namespace_allow override")
	}
	// subdelegation deny from the coder preset features survives the merge.
	if ok, _ := p.Allows("mcpx__delegate_worker", false); ok {
		t.Error("merged profile lost coder's subdelegation deny")
	}
}

func TestValidateCapabilityProfileJSON(t *testing.T) {
	if err := validateCapabilityProfileJSON(""); err != nil {
		t.Errorf("empty JSON rejected: %v", err)
	}
	if err := validateCapabilityProfileJSON("null"); err != nil {
		t.Errorf("null JSON rejected: %v", err)
	}
	if err := validateCapabilityProfileJSON(`{"namespace_allow":["github"]}`); err != nil {
		t.Errorf("valid profile rejected: %v", err)
	}
	if err := validateCapabilityProfileJSON(`{"namespace_allow":["mcplexer"]}`); err == nil {
		t.Error("admin-namespace profile accepted")
	}
	if err := validateCapabilityProfileJSON(`not json`); err == nil {
		t.Error("malformed JSON accepted")
	}
}

func TestDelegationCapabilityPresetLabel(t *testing.T) {
	if got := delegationCapabilityPresetLabel(DelegationInput{}); got != "" {
		t.Errorf("no-scope label = %q, want empty", got)
	}
	if got := delegationCapabilityPresetLabel(DelegationInput{CapabilityPreset: "Coder"}); got != "coder" {
		t.Errorf("preset label = %q, want coder", got)
	}
	got := delegationCapabilityPresetLabel(DelegationInput{
		CapabilityProfile: &toolgate.CapabilityProfile{},
	})
	if got != "custom" {
		t.Errorf("ad-hoc label = %q, want custom", got)
	}
}

func TestResolvedProfileMarshalsCompact(t *testing.T) {
	// Sanity: the marshalled minimal preset is a small object, not the
	// whole struct with nil fields exploded.
	in := &DelegationInput{CapabilityPreset: "minimal"}
	if err := resolveDelegationCapabilityProfile(in); err != nil {
		t.Fatalf("resolve minimal: %v", err)
	}
	if !strings.Contains(in.capabilityProfileJSON, `"namespace_allow":["mcpx"]`) {
		t.Errorf("minimal profile JSON missing mcpx namespace_allow: %s", in.capabilityProfileJSON)
	}
}
