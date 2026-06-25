package gateway

import "testing"

// TestIsTrustedBuiltinResult is the public-facing accessor used by
// handler_tools.go to gate the structuredContent lift. It must agree
// with classifyTrust(...).NeedsSanitize=false.
func TestIsTrustedBuiltinResult(t *testing.T) {
	cases := []struct {
		tool  string
		want  bool
		group string
	}{
		{"task__get", true, "trusted builtin"},
		{"mcpx__search_tools", true, "trusted builtin"},
		{"memory__recall", true, "trusted builtin"},
		{"secret__prompt", true, "trusted builtin"},
		{"mcplexer__status", true, "trusted builtin"},
		{"mesh__list_agents", true, "local mesh"},
		{"mesh__send", true, "local mesh"},
		{"mesh__receive", false, "peer-origin"},
		{"mesh__hydrate", false, "peer-origin"},
		{"mesh__thread", false, "peer-origin"},
		{"linear__search", false, "downstream"},
		{"playwright__browser_evaluate", false, "downstream"},
		{"brw_chromium__brw_list_tabs", true, "trusted downstream metadata"},
		{"brw_chromium__brw_read", false, "browser content"},
	}
	for _, c := range cases {
		t.Run(c.group+":"+c.tool, func(t *testing.T) {
			if got := isTrustedBuiltinResult(c.tool); got != c.want {
				t.Errorf("isTrustedBuiltinResult(%q) = %v, want %v",
					c.tool, got, c.want)
			}
		})
	}
}

func TestClassifyTrust_BrowserStructuralMetadataSkipsSanitize(t *testing.T) {
	for _, name := range []string{
		"brw__brw_open",
		"brw__brw_list_tabs",
		"brw__brw_list_tab_groups",
		"brw_chromium__brw_open",
		"brw_chromium__brw_list_tabs",
		"brw_chromium__brw_list_tab_groups",
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyTrust(name)
			if got.NeedsSanitize {
				t.Errorf("NeedsSanitize = true, want false for structural metadata")
			}
			if got.TrustLevel != "high" {
				t.Errorf("TrustLevel = %q, want high", got.TrustLevel)
			}
		})
	}
}

func TestClassifyTrust_BrowserContentStillSanitizes(t *testing.T) {
	for _, name := range []string{
		"brw__brw_read",
		"brw__brw_find",
		"brw__brw_screenshot",
		"brw_chromium__brw_read",
		"brw_chromium__brw_find",
		"brw_chromium__brw_screenshot",
	} {
		t.Run(name, func(t *testing.T) {
			got := classifyTrust(name)
			if !got.NeedsSanitize {
				t.Errorf("NeedsSanitize = false, want true for browser content")
			}
			if got.TrustLevel != "low" {
				t.Errorf("TrustLevel = %q, want low", got.TrustLevel)
			}
		})
	}
}

// TestClassifyTrust_BuiltinPrefixesSkipSanitize covers the H1 ergonomics
// fix: the four canonical trusted builtin namespaces (mcpx__/task__/
// memory__/secret__/mcplexer__) must report NeedsSanitize=false so the
// gateway short-circuits the envelope pipeline and the calling LLM sees
// the raw text — no <untrusted-content> wrapper, no HTML entities, no
// tax on what is (by construction) gateway-internal payload.
func TestClassifyTrust_BuiltinPrefixesSkipSanitize(t *testing.T) {
	cases := []string{
		"mcpx__search_tools",
		"mcpx__execute_code",
		"mcpx__provision_mcp",
		"task__get",
		"task__list",
		"task__update",
		"task__append_note",
		"memory__save",
		"memory__recall",
		"secret__prompt",
		"mcplexer__list_workspaces",
		"mcplexer__status",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyTrust(name)
			if got.NeedsSanitize {
				t.Errorf("NeedsSanitize = true, want false (trusted builtin)")
			}
			if got.ForceEnvelope {
				t.Errorf("ForceEnvelope = true, want false (trusted builtin)")
			}
			if got.TrustLevel != "high" {
				t.Errorf("TrustLevel = %q, want %q", got.TrustLevel, "high")
			}
		})
	}
}

// TestClassifyTrust_MeshLocalToolsSkipSanitize pins the local-only mesh
// tools (list_*, send, scope ops) as trusted. These operate on
// in-process registries / settings, not on cross-peer payloads.
func TestClassifyTrust_MeshLocalToolsSkipSanitize(t *testing.T) {
	cases := []string{
		"mesh__list_agents",
		"mesh__list_peers",
		"mesh__list_queue",
		"mesh__list_pending_secrets",
		"mesh__set_agent_status",
		"mesh__set_device_name",
		"mesh__grant_peer_scope",
		"mesh__revoke_peer_scope",
		"mesh__send",
		"mesh__send_secret",
		"mesh__accept_secret",
		"mesh__reject_secret",
		"mesh__offer_skill",
		"mesh__request_skill",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyTrust(name)
			if got.NeedsSanitize {
				t.Errorf("NeedsSanitize = true, want false (local mesh tool)")
			}
		})
	}
}

// TestClassifyTrust_MeshReadsAlwaysEnvelope is the safety pin: mesh reads
// whose payload includes cross-peer content must force-envelope regardless of
// dashboard toggles, with trust="peer" so downstream policy can distinguish
// mesh ingest from generic low-trust web scrape.
func TestClassifyTrust_MeshReadsAlwaysEnvelope(t *testing.T) {
	for _, name := range []string{"mesh__receive", "mesh__hydrate", "mesh__thread"} {
		t.Run(name, func(t *testing.T) {
			got := classifyTrust(name)
			if !got.NeedsSanitize {
				t.Errorf("NeedsSanitize = false, want true (peer-origin tool)")
			}
			if !got.ForceEnvelope {
				t.Errorf("ForceEnvelope = false, want true (peer-origin tool)")
			}
			if got.TrustLevel != "peer" {
				t.Errorf("TrustLevel = %q, want %q", got.TrustLevel, "peer")
			}
			if got.Source != "tool:"+name {
				t.Errorf("Source = %q, want %q", got.Source, "tool:"+name)
			}
		})
	}
}

// TestClassifyTrust_DownstreamToolsSanitize covers the default case:
// anything that isn't a known-trusted builtin (downstream MCP servers,
// chat__/email__ bridges, addon-namespaced tools) must go through the
// sanitize pipeline with trust="low".
func TestClassifyTrust_DownstreamToolsSanitize(t *testing.T) {
	cases := []string{
		"github__list_issues",
		"linear__search",
		"slack__post_message",
		"customer__get_ticket",
		"chat__send_message",
		"email__send",
		"playwright__browser_evaluate",
		"hammerspoon__list_windows", // addon namespace
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyTrust(name)
			if !got.NeedsSanitize {
				t.Errorf("NeedsSanitize = false, want true (downstream tool)")
			}
			if got.ForceEnvelope {
				t.Errorf("ForceEnvelope = true, want false (downstream — only envelopes on hit / envelope-always)")
			}
			if got.TrustLevel != "low" {
				t.Errorf("TrustLevel = %q, want %q", got.TrustLevel, "low")
			}
		})
	}
}
