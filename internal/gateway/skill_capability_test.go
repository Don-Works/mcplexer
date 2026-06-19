package gateway

import (
	"context"
	"errors"
	"testing"
)

// TestCheckSkillAllowlist drives the allow/deny matrix table-style.
// The skill_id is irrelevant to the check itself — only the namespace
// allowlist set on the context matters.
func TestCheckSkillAllowlist(t *testing.T) {
	cases := []struct {
		name     string
		allow    []string // nil = no skill context (always allow)
		toolName string
		wantErr  bool
	}{
		{
			name:     "no skill context — direct call passes through",
			allow:    nil,
			toolName: "linear__list_issues",
			wantErr:  false,
		},
		{
			name:     "namespace in allowlist — allowed",
			allow:    []string{"freeagent", "browser"},
			toolName: "browser__navigate",
			wantErr:  false,
		},
		{
			name:     "first-position match",
			allow:    []string{"freeagent"},
			toolName: "freeagent__create_invoice",
			wantErr:  false,
		},
		{
			name:     "namespace not in allowlist — denied",
			allow:    []string{"freeagent"},
			toolName: "linear__list_issues",
			wantErr:  true,
		},
		{
			name:     "empty allowlist with downstream call — denied",
			allow:    []string{},
			toolName: "browser__navigate",
			wantErr:  true,
		},
		{
			name:     "mcpx builtins always allowed",
			allow:    []string{"freeagent"},
			toolName: "mcpx__search_tools",
			wantErr:  false,
		},
		{
			name:     "mesh builtins always allowed",
			allow:    []string{"freeagent"},
			toolName: "mesh__send",
			wantErr:  false,
		},
		{
			name:     "un-namespaced tool name — defer to other gates",
			allow:    []string{"freeagent"},
			toolName: "no_namespace_tool",
			wantErr:  false,
		},
		{
			name:     "case-sensitive: capital does not match lowercase",
			allow:    []string{"freeagent"},
			toolName: "FreeAgent__list",
			wantErr:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.allow != nil {
				ctx = withSkillID(ctx, "test-skill")
				ctx = withSkillAllowlist(ctx, tc.allow)
			}
			err := checkSkillAllowlist(ctx, tc.toolName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("checkSkillAllowlist(%q) = nil, want denial", tc.toolName)
				}
				if !errors.Is(err, ErrCapabilityDenied) {
					t.Fatalf("err = %v, want errors.Is(ErrCapabilityDenied)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkSkillAllowlist(%q) = %v, want nil", tc.toolName, err)
			}
		})
	}
}

// TestSkillContextHelpers verifies the helpers round-trip values and
// return zero values when no skill context is attached.
func TestSkillContextHelpers(t *testing.T) {
	ctx := context.Background()

	if id := skillIDFromContext(ctx); id != "" {
		t.Errorf("empty ctx skillID = %q, want empty", id)
	}
	if al := skillAllowlistFromContext(ctx); al != nil {
		t.Errorf("empty ctx allowlist = %v, want nil", al)
	}

	ctx = withSkillID(ctx, "blog-post")
	ctx = withSkillAllowlist(ctx, []string{"github", "linear"})

	if id := skillIDFromContext(ctx); id != "blog-post" {
		t.Errorf("skillID = %q, want %q", id, "blog-post")
	}
	al := skillAllowlistFromContext(ctx)
	if len(al) != 2 || al[0] != "github" || al[1] != "linear" {
		t.Errorf("allowlist = %v, want [github linear]", al)
	}
}
