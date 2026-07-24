package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestHarnessProfileForClient(t *testing.T) {
	tests := []struct {
		client string
		want   HarnessProfile
	}{
		{"", HarnessDirect},
		{"claude-code", HarnessDirect},
		{"Claude Code", HarnessDirect},
		{"codex-cli", HarnessDirect},
		{"opencode", HarnessDirect},
		{"grok-cli", HarnessServerPrefixed},
		{"xAI Grok", HarnessServerPrefixed},
		{"cursor-vscode", HarnessServerPrefixed},
		{"Cursor", HarnessServerPrefixed},
		{"windsurf", HarnessServerPrefixed},
		{"gemini-cli", HarnessServerPrefixed},
	}
	for _, tc := range tests {
		if got := harnessProfileForClient(tc.client); got != tc.want {
			t.Errorf("harnessProfileForClient(%q) = %v, want %v", tc.client, got, tc.want)
		}
	}
}

func TestResolveHarnessToolName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"mcpx__execute_code", "mcpx__execute_code"},
		{"mcpx__search_tools", "mcpx__search_tools"},
		{"mcpx__call_tool", "mcpx__call_tool"},
		{"secret__prompt", "secret__prompt"},
		{"execute_code", "mcpx__execute_code"},
		{"search_tools", "mcpx__search_tools"},
		{"call_tool", "mcpx__call_tool"},
		{"prompt", "secret__prompt"},
		{"list_refs", "secret__list_refs"},
		{"mcplexer__execute_code", "mcpx__execute_code"},
		{"mcplexer__search_tools", "mcpx__search_tools"},
		{"mcplexer__call_tool", "mcpx__call_tool"},
		{"mcplexer__prompt", "secret__prompt"},
		{"mcplexer__list_refs", "secret__list_refs"},
		{"mcplexer__mcpx__execute_code", "mcpx__execute_code"},
		{"mcplexer__mcpx__search_tools", "mcpx__search_tools"},
		{"mcplexer__mcpx__call_tool", "mcpx__call_tool"},
		{"mcplexer__secret__prompt", "secret__prompt"},
		{"mx__execute_code", "mcpx__execute_code"},
		{"mcplexer__execute_code", "mcpx__execute_code"},
		// Legacy mcplexer__ rename path still works for the mcpx builtins.
		{"mcplexer__search_tools", "mcpx__search_tools"},
		// Admin/control names must NOT be stripped into bare names.
		{"mcplexer__list_workspaces", "mcplexer__list_workspaces"},
		{"mcplexer__delete_route", "mcplexer__delete_route"},
		{"mcplexer__create_worker", "mcplexer__create_worker"},
		{"mcplexer__mcpx__execute_code", "mcpx__execute_code"},
		{"mx__delete_route", "mcplexer__delete_route"},
		{"mx__list_workspaces", "mcplexer__list_workspaces"},
		// Double-qualified admin tools resolve correctly.
		{"mcplexer__mcplexer__list_workspaces", "mcplexer__list_workspaces"},
		// Namespaced tools after prefix stripping pass through.
		{"mcplexer__mesh__send", "mesh__send"},
		{"mcplexer__memory__save", "memory__save"},
		{"mx__task__create", "task__create"},
	}
	for _, tc := range tests {
		if got := resolveHarnessToolName(tc.in); got != tc.want {
			t.Errorf("resolveHarnessToolName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestApplyHarnessToolListNames_Grok(t *testing.T) {
	in := []Tool{
		{Name: "mcpx__execute_code"},
		{Name: "mcpx__search_tools"},
		{Name: "mcpx__call_tool"},
		{Name: "secret__prompt"},
		{Name: "secret__list_refs"},
		{Name: "mcpx__retrieve"},
	}
	got := applyHarnessToolListNames(HarnessServerPrefixed, in)
	want := []string{"execute_code", "search_tools", "call_tool", "prompt", "list_refs", "retrieve"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("tool[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
}

func TestApplyHarnessToolListNames_ClaudeUntouched(t *testing.T) {
	in := []Tool{{Name: "mcpx__execute_code"}}
	got := applyHarnessToolListNames(HarnessDirect, in)
	if got[0].Name != "mcpx__execute_code" {
		t.Errorf("direct harness renamed tool: %q", got[0].Name)
	}
}

func TestHandleToolsList_GrokAliases(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.sessions.session = &store.Session{ClientType: "grok-cli"}

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("handleToolsList: %v", rpcErr.Message)
	}
	names := toolNames(result)
	// Test handler omits secret__* when secret services are nil; still verify
	// every returned slim-surface name is a single-segment harness alias.
	for _, name := range names {
		if strings.Contains(name, "__") {
			t.Errorf("grok tools/list still has double-underscore name %q (all: %v)", name, names)
		}
	}
	for _, want := range []string{"execute_code", "search_tools", "call_tool"} {
		if !containsString(names, want) {
			t.Errorf("missing harness alias %q in %v", want, names)
		}
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestBuildCodeModeInstructions(t *testing.T) {
	required := []string{
		"full JavaScript environment",
		"math, parsing, dedupe, date arithmetic",
		"with or without tool calls",
		"`sleep(ms)` clamps each call to 60000ms",
		"while(!done){ sleep(2000); ... }",
		"`parallel()` returns null entries for failed calls and does not throw",
		"check for null",
		"auto-unwrapped",
		"`brw`/browser tools first",
		"browser skill",
	}
	profiles := []struct {
		name    string
		profile HarnessProfile
		want    []string
	}{
		{
			name:    "direct",
			profile: HarnessDirect,
			want: []string{
				"`mcpx__execute_code`",
				"`mcpx__search_tools`",
				"`mcpx__call_tool`",
				"`mcpx__retrieve`",
				"`mcpx.skill_get",
			},
		},
		{
			name:    "server_prefixed",
			profile: HarnessServerPrefixed,
			want: []string{
				"`mcplexer__execute_code`",
				"`mcplexer__search_tools`",
				"`mcplexer__call_tool`",
				"`mcplexer__retrieve`",
			},
		},
	}
	for _, tc := range profiles {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCodeModeInstructions(tc.profile, false)
			for _, sub := range append(required, tc.want...) {
				if !strings.Contains(got, sub) {
					t.Errorf("instructions missing %q", sub)
				}
			}
		})
	}
}

func TestBuildCodeModeInstructions_MeshEnabled(t *testing.T) {
	direct := buildCodeModeInstructions(HarnessDirect, true)
	if !strings.Contains(direct, "mcpx__execute_code") || !strings.Contains(direct, "mesh.receive") {
		t.Error("direct mesh instructions missing dynamic mesh usage")
	}
	if strings.Contains(direct, "Call `mesh__receive`") || strings.Contains(direct, "Call `mesh__send`") {
		t.Error("direct mesh instructions still advertise top-level mesh calls")
	}
	prefixed := buildCodeModeInstructions(HarnessServerPrefixed, true)
	if !strings.Contains(prefixed, "mcplexer__execute_code") {
		t.Error("prefixed mesh instructions missing mcplexer__execute_code")
	}
}

// TestBuildCompactCodeModeInstructions_KeepsLoadBearingRules asserts the
// compact variant is a narrowing, not a rewrite: every rule a worker cannot
// write a correct snippet without has to survive the cut.
func TestBuildCompactCodeModeInstructions_KeepsLoadBearingRules(t *testing.T) {
	got := buildCompactCodeModeInstructions(false)
	for _, must := range []string{
		"mcpx__execute_code",           // how to run anything
		"mcpx__search_tools",           // how to find anything
		"mcpx__call_tool",              // cheap path for one independent call
		"mcpx__retrieve",               // how to expand a CCR marker
		"`<namespace>.<tool>(args)`",   // the call form
		"synchronous",                  // no await
		"auto-unwrapped",               // do not JSON.parse the envelope
		"JSON.parse(result.content[0]", // the exact anti-pattern
		"ONE snippet",                  // batching
		"NEVER print raw responses",    // output discipline
		"returns null entries",         // parallel() contract
	} {
		if !strings.Contains(got, must) {
			t.Errorf("compact instructions dropped load-bearing rule %q\n--- got ---\n%s", must, got)
		}
	}
	// The point of the variant is cost. Anything close to the full text means
	// the trim silently stopped paying for itself.
	full := buildCodeModeInstructions(HarnessDirect, false)
	if len(got)*2 > len(full) {
		t.Errorf("compact instructions = %d B, want well under half of full (%d B)", len(got), len(full))
	}
}

// TestBuildCodeModeInstructionsForClient_PiOnly proves the compact variant is
// scoped to Pi. Every other harness — including lookalike names that must not
// match isPiHarness — keeps the full instructions byte-for-byte.
func TestBuildCodeModeInstructionsForClient_PiOnly(t *testing.T) {
	compact := buildCompactCodeModeInstructions(true)
	for _, name := range []string{"pi", "pi-coding-agent", "@mariozechner/pi-coding-agent", "pi.dev", "earendil"} {
		if got := buildCodeModeInstructionsForClient(name, true); got != compact {
			t.Errorf("client %q should get the compact instructions", name)
		}
	}
	direct := buildCodeModeInstructions(HarnessDirect, true)
	for _, name := range []string{"claude-code", "codex", "opencode", "raspberry-pi", "copilot", ""} {
		if got := buildCodeModeInstructionsForClient(name, true); got != direct {
			t.Errorf("client %q must keep the full direct instructions", name)
		}
	}
	prefixed := buildCodeModeInstructions(HarnessServerPrefixed, true)
	for _, name := range []string{"grok-cli", "cursor", "windsurf", "gemini-cli", "picoclaw"} {
		if got := buildCodeModeInstructionsForClient(name, true); got != prefixed {
			t.Errorf("client %q must keep the full server-prefixed instructions", name)
		}
	}
}

// TestBuildCompactCodeModeInstructions_Mesh keeps the mesh hint conditional,
// matching the full variant — a worker on a mesh-less gateway shouldn't pay
// for instructions about a surface it cannot reach.
func TestBuildCompactCodeModeInstructions_Mesh(t *testing.T) {
	if strings.Contains(buildCompactCodeModeInstructions(false), "mesh.receive") {
		t.Error("mesh hint leaked into the mesh-disabled compact instructions")
	}
	if !strings.Contains(buildCompactCodeModeInstructions(true), "mesh.receive") {
		t.Error("mesh-enabled compact instructions missing mesh.receive")
	}
}

func TestHandleToolsList_ClaudeCanonical(t *testing.T) {
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.sessions.session = &store.Session{ClientType: "claude-code"}

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("handleToolsList: %v", rpcErr.Message)
	}
	names := toolNames(result)
	if len(names) == 0 {
		t.Fatal("claude tools/list is empty")
	}
	for _, name := range names {
		if name == "call_tool" || name == "execute_code" || name == "search_tools" {
			t.Errorf("claude tools/list = %v, want canonical names", names)
		}
	}
}
