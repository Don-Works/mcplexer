package mesh

import (
	"regexp"
	"strings"

	meshpkg "github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// matchesTrigger returns true when every configured filter on t admits
// msg. Empty filters are treated as "any" — a trigger with no filters
// matches every message (the dispatcher requires that the admin
// validate this is intentional, but the matcher doesn't second-guess).
func matchesTrigger(t *store.WorkerMeshTrigger, msg *store.MeshMessage) bool {
	if !matchesKind(t.KindMatch, msg.Kind) {
		return false
	}
	if !matchesAudience(t.AudienceMatch, msg.Audience) {
		return false
	}
	if !matchesTags(t.TagMatch, msg.Tags) {
		return false
	}
	if !matchesContentRegex(t.ContentRegex, msg.Content) {
		return false
	}
	if !matchesStatusTransition(t, msg) {
		return false
	}
	if !matchesFromFilters(t.FromFilters, msg) {
		return false
	}
	return true
}

// matchesStatusTransition AND's the trigger's StatusFromMatch /
// StatusToMatch (each empty = "any") against the transition carried on a
// task_event:status_changed message via its status_from:/status_to:
// tags. Non-status messages carry neither tag, so a trigger that sets
// either field is implicitly scoped to status-change events — and a
// status event whose from/to differs is rejected. This is the AND that
// the OR-semantics TagMatch can't express (e.g. "status_changed AND
// to=review").
func matchesStatusTransition(t *store.WorkerMeshTrigger, msg *store.MeshMessage) bool {
	if t.StatusFromMatch == "" && t.StatusToMatch == "" {
		return true
	}
	// A status constraint only applies to genuine status-change events.
	// Require the canonical task_event:status_changed tag so neither a
	// non-status message nor a task carrying a spoofed "status_to:" label
	// can satisfy a transition trigger. Defense-in-depth: the emitter's
	// buildEventTags strips reserved-prefix user tags, so the only source
	// of status_from:/status_to:/task_event: tags is the emitter itself.
	if !hasTag(msg.Tags, "task_event:status_changed") {
		return false
	}
	from, to := statusTransition(msg.Tags)
	if t.StatusFromMatch != "" && t.StatusFromMatch != from {
		return false
	}
	if t.StatusToMatch != "" && t.StatusToMatch != to {
		return false
	}
	return true
}

// statusTransition extracts the status_from:/status_to: tag values the
// tasks event emitter stamps on status-change messages. Returns
// ("", "") when absent (any non-status-change message). Mirrors
// sourcePeerID's "from:" parsing so both read tags the same way.
func statusTransition(tags string) (from, to string) {
	for _, tag := range splitTags(tags) {
		if rest, ok := strings.CutPrefix(tag, "status_from:"); ok {
			from = strings.TrimSpace(rest)
		} else if rest, ok := strings.CutPrefix(tag, "status_to:"); ok {
			to = strings.TrimSpace(rest)
		}
	}
	return from, to
}

// matchesKind: empty trigger filter matches anything; otherwise
// equality.
func matchesKind(filter, msgKind string) bool {
	if filter == "" {
		return true
	}
	return filter == msgKind
}

// matchesAudience: "*" or empty matches anything. Otherwise equality
// against MeshMessage.Audience (which is itself "*", a role name, or
// session-id).
func matchesAudience(filter, msgAudience string) bool {
	if filter == "" || filter == "*" {
		return true
	}
	return filter == msgAudience
}

// matchesTags: trigger's TagMatch is a comma-separated set; if ANY tag
// in the message's tag list appears in the filter set, the trigger
// matches. Empty trigger filter matches anything.
func matchesTags(filter, msgTags string) bool {
	wanted := splitTags(filter)
	if len(wanted) == 0 {
		return true
	}
	have := splitTags(msgTags)
	if len(have) == 0 {
		return false
	}
	for _, h := range have {
		for _, w := range wanted {
			if h == w {
				return true
			}
		}
	}
	return false
}

// matchesContentRegex: empty trigger regex matches anything. Invalid
// regexes are treated as non-matching with a stderr-style log so the
// admin can fix the row; the dispatcher itself never crashes.
func matchesContentRegex(pattern, content string) bool {
	if pattern == "" {
		return true
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(content)
}

// matchesFromFilters: ANY of the filters matching admits the message.
// Empty filter list = "anyone allowed".
//
// A filter matches when EVERY non-empty field on the filter equals the
// corresponding field on the message:
//   - peer_id matches sourcePeerID(msg); "self" matches local-origin.
//   - agent_name matches MeshMessage.AgentName.
//   - role is NOT implemented: mesh messages don't carry the sender's
//     role on the wire, so a role constraint can never be verified at
//     match time. A filter with a non-empty Role therefore FAILS CLOSED
//     (admits nothing) — admin validation rejects new rows that set it,
//     and any legacy row that slips through must not silently widen
//     into "everyone allowed" (which is how the old best-effort branch
//     behaved, and role-restricted triggers can drive NetworkHost CLI
//     workers).
func matchesFromFilters(filters []store.TriggerFromFilter, msg *store.MeshMessage) bool {
	if len(filters) == 0 {
		return true
	}
	source := sourcePeerID(msg)
	for _, f := range filters {
		if filterAdmits(f, msg, source) {
			return true
		}
	}
	return false
}

// filterAdmits implements the per-filter conjunctive match. An empty
// filter (all fields blank) is treated as a wildcard admit so admins
// can persist an "any source allowed" entry explicitly.
func filterAdmits(f store.TriggerFromFilter, msg *store.MeshMessage, source string) bool {
	if f.Role != "" {
		// Role filtering is not implemented — fail closed. See
		// matchesFromFilters.
		return false
	}
	if f.PeerID == "" && f.AgentName == "" {
		return true
	}
	if f.PeerID != "" {
		if f.PeerID == "self" {
			if source != "" {
				return false
			}
		} else if f.PeerID != source {
			return false
		}
	}
	if f.AgentName != "" && f.AgentName != msg.AgentName {
		return false
	}
	return true
}

// hasTag reports whether the comma-separated tag list contains an exact
// tag (after trimming). Used to gate status-transition matching on the
// canonical task_event:status_changed marker.
func hasTag(tags, want string) bool {
	for _, tag := range splitTags(tags) {
		if tag == want {
			return true
		}
	}
	return false
}

// splitTags turns a comma-separated string into a trimmed []string.
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sourcePeerID returns the peer ID this message arrived from. Inbound
// p2p messages tag themselves "p2p,from:<peer_id>" via the ingest path
// in p2p_bridge.go; local sends use the session as the agent identity
// and do NOT carry a from: tag. Returns "" for local messages.
func sourcePeerID(msg *store.MeshMessage) string {
	for _, tag := range splitTags(msg.Tags) {
		if rest, ok := strings.CutPrefix(tag, "from:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// chainDepthFromTags forwards to the canonical helper in
// internal/mesh so both the dispatcher and the tasks event emitter
// read the same parser. Pre-Phase-2 implementations of this duplicated
// the regex locally; consolidating keeps the format invariant.
func chainDepthFromTags(tags string) int {
	return meshpkg.ChainDepthFromTags(tags)
}

// ChainDepthTag forwards to the canonical renderer in internal/mesh.
// Exposed so the runner-side helper imports here rather than reaching
// across packages (preserves the existing internal/workers/runner
// import path).
func ChainDepthTag(depth int) string {
	return meshpkg.ChainDepthTag(depth)
}
