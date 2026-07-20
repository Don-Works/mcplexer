package gateway

import "strings"

// TrustClassification describes how the gateway should treat the result
// of a tool call before shipping it to the calling LLM.
//
// The two questions a caller actually needs answered are:
//   - "must I sanitize this output for prompt-injection markers?" — yes
//     for anything ingesting third-party / network / peer content, no for
//     gateway-internal admin reads whose payload the gateway itself owns.
//   - "must I unconditionally envelope clean output even on a no-hit scan?"
//     — only when settings.SanitizerEnvelopeAlways is on AND the namespace
//     is in the untrusted set. Trusted internal tools should never wear
//     the <untrusted-content> tag even with envelope-always toggled on;
//     that toggle is a defence-in-depth knob for external content.
type TrustClassification struct {
	// NeedsSanitize is true when the gateway should run sanitize.Process
	// over the tool result's text content. False short-circuits the
	// entire sanitize stage for trusted-internal namespaces.
	NeedsSanitize bool

	// ForceEnvelope is true when the result MUST be enveloped regardless
	// of denylist hits or the SanitizerEnvelopeAlways setting. Set for
	// tools whose payload is by-definition cross-peer / cross-machine
	// (mesh__receive, chat__/email__ ingest paths) so the LLM always
	// sees the trust marker even if the content looks benign.
	ForceEnvelope bool

	// Source is the canonical source attribute value emitted into the
	// <untrusted-content source="…"> tag when the result IS enveloped.
	// Always set so callers don't have to recompute it.
	Source string

	// TrustLevel is the asserted trust attribute. "low" for downstream
	// MCP servers (the safe default), "peer" for mesh-ingested content
	// originating from another machine, "high" for in-process trusted
	// builtins (which never get enveloped — included for completeness).
	TrustLevel string
}

// trustedBuiltinPrefixes enumerates the in-process namespaces whose
// payload is constructed entirely by the gateway from its own database
// or memory state, never by parsing third-party content. These results
// have no prompt-injection surface — anything that LOOKS like an
// injection in here would have to come from a previously-written user
// note or task description, which the user themselves authored. Wrapping
// those in <untrusted-content> is pure token tax.
//
// Notably absent: `mesh__` (which CAN ingest cross-peer payloads) and
// the `chat__` / `email__` bridges (which ingest external message bodies).
var trustedBuiltinPrefixes = []string{
	BuiltinPrefix,       // mcpx__ — code execution + admin
	TaskPrefix,          // task__ — local + cross-peer task system
	MemoryPrefix,        // memory__ — local memory store
	SecretPrefix,        // secret__ — server-side secret prompts (already redacted)
	legacyBuiltinPrefix, // mcplexer__ — legacy admin surface
}

// meshTrustedTools enumerates the mesh__ tools whose payload the gateway
// builds entirely from local state the local user authored — scope grants,
// queue counters, echoes of the caller's own input. They carry no
// peer-authored free text, so wrapping them is pure token tax.
//
// The bar is "could a REMOTE machine have written any string in this
// output?", and it is easy to get wrong: list_peers, list_agents, and
// list_pending_secrets used to sit in this map on the theory that peer
// registries are "local session state". They are not — the rows are local
// but the display names, agent names, and offer metadata inside them are
// peer-authored, and the remote rename path applied none of the validation
// the local one did. They now live in peerOriginTools with the other
// cross-peer reads.
var meshTrustedTools = map[string]bool{
	MeshPrefix + "list_queue":        true,
	MeshPrefix + "set_agent_status":  true,
	MeshPrefix + "set_device_name":   true,
	MeshPrefix + "grant_peer_scope":  true,
	MeshPrefix + "revoke_peer_scope": true,
	MeshPrefix + "accept_secret":     true,
	MeshPrefix + "reject_secret":     true,
	// mesh__send echoes the agent's own outbound payload back as a
	// receipt — same provenance as task__create echoing a task title,
	// so trusted.
	MeshPrefix + "send":          true,
	MeshPrefix + "send_secret":   true,
	MeshPrefix + "offer_skill":   true,
	MeshPrefix + "request_skill": true,
}

// peerOriginTools enumerates tools whose payload is BY DEFINITION
// cross-peer / cross-machine ingest. We force-envelope these even when
// the denylist is silent, because the threat model is "another machine
// fed us free text" — and that ALWAYS warrants the trust marker.
//
// Builtin results never reach sanitizeToolResult (they return early in
// handler_tools.go), so this classification only takes effect where a
// handler applies it itself via meshFieldSanitizer. Every tool listed
// here must do so.
var peerOriginTools = map[string]bool{
	MeshPrefix + "receive":        true,
	MeshPrefix + "wait_for_event": true,
	MeshPrefix + "hydrate":        true,
	MeshPrefix + "thread":         true,
	// Directory / offer listings: the ROWS are local, but display names,
	// agent names, and offer metadata inside them are peer-authored.
	MeshPrefix + "list_peers":           true,
	MeshPrefix + "list_agents":          true,
	MeshPrefix + "list_pending_secrets": true,
}

// trustedDownstreamMetadataTools enumerates downstream tools whose outputs
// are gateway/browser-control metadata rather than page body content. They
// should stay clean JSON even when the sanitizer envelope-always toggle is
// enabled, so code-mode can consume ids, tab lists, and group rows directly.
var trustedDownstreamMetadataTools = map[string]bool{
	"brw__brw_open":                     true,
	"brw__brw_list_tabs":                true,
	"brw__brw_list_tab_groups":          true,
	"brw__brw_focus_tab":                true,
	"brw__brw_group_tabs":               true,
	"brw__brw_close_tab":                true,
	"brw__brw_navigate":                 true,
	"brw_chromium__brw_open":            true,
	"brw_chromium__brw_list_tabs":       true,
	"brw_chromium__brw_list_tab_groups": true,
	"brw_chromium__brw_focus_tab":       true,
	"brw_chromium__brw_group_tabs":      true,
	"brw_chromium__brw_close_tab":       true,
	"brw_chromium__brw_navigate":        true,
}

// isTrustedBuiltinResult reports whether the tool's result comes from
// a trusted in-process builtin (mcpx__/task__/memory__/secret__/
// mcplexer__/mesh-local-tools). Used by the handler to decide whether
// to skip the structuredContent lift — trusted builtins ship clean
// JSON in the text slot already, so adding structuredContent doubles
// the wire bytes without giving the agent additional information.
//
// Note: mesh__receive is NOT trusted by this gate. Its text content is
// peer-origin data wrapped in <untrusted-content>, and the lift would
// be skipped at the surfaceStructuredContent level anyway (it detects
// the envelope prefix).
func isTrustedBuiltinResult(toolName string) bool {
	return !classifyTrust(toolName).NeedsSanitize
}

// classifyTrust decides whether the result of a tool call requires
// the sanitize / envelope pipeline.
//
// Rules, in order:
//
//  1. Peer-origin mesh read tools → sanitize + force-envelope with
//     trust="peer". The wrapper is load-bearing here regardless of
//     denylist hits because the payload is by-definition cross-machine.
//
//  2. Trusted mesh tool (list_*, send, scope, …) → no sanitize. These
//     operate on local state without surfacing cross-peer free-text.
//
//  3. Trusted builtin prefix (mcpx__/task__/memory__/secret__/mcplexer__)
//     → no sanitize, no wrapper. The payload comes from the gateway's
//     own state.
//
//  4. Everything else (downstream MCP servers, chat__/email__ bridges,
//     addon tools) → sanitize with trust="low". The source label
//     reflects the actual namespace so the envelope attribute is honest.
func classifyTrust(toolName string) TrustClassification {
	src := "tool:" + toolName

	if peerOriginTools[toolName] {
		return TrustClassification{
			NeedsSanitize: true,
			ForceEnvelope: true,
			Source:        src,
			TrustLevel:    "peer",
		}
	}

	if meshTrustedTools[toolName] {
		return TrustClassification{
			NeedsSanitize: false,
			Source:        src,
			TrustLevel:    "high",
		}
	}

	for _, p := range trustedBuiltinPrefixes {
		if strings.HasPrefix(toolName, p) {
			return TrustClassification{
				NeedsSanitize: false,
				Source:        src,
				TrustLevel:    "high",
			}
		}
	}

	if trustedDownstreamMetadataTools[toolName] {
		return TrustClassification{
			NeedsSanitize: false,
			Source:        src,
			TrustLevel:    "high",
		}
	}

	return TrustClassification{
		NeedsSanitize: true,
		Source:        src,
		TrustLevel:    "low",
	}
}
