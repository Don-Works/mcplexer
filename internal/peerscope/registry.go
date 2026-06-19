// Package peerscope is the canonical registry of scope strings that can
// be granted to paired libp2p peers via mesh__grant_peer_scope.
//
// Scopes are short opaque strings the gateway stores on p2p_peers.scopes;
// individual subsystems (workers, p2p memory share, tasks) decide which
// scope they require for which action and call store.HasPeerScope(...)
// to enforce.
//
// This registry exists so the UI + agent can answer "what scopes COULD I
// grant?" without each subsystem hand-rolling its own discovery path.
// Subsystems still own their concrete prefix constants (e.g.
// triggerScopePrefix in workers/triggers/mesh/dispatcher.go); the
// registry is a parallel canonical list intended for display + grant
// pickers. The peerscope_consistency_test.go test asserts that every
// const string in the codebase corresponds to a Known entry.
package peerscope

// ScopeDef describes one grantable scope shape.
type ScopeDef struct {
	// Prefix is the literal scope string (boolean scopes) or the
	// colon-prefix shape that gets a resource suffix appended at grant
	// time (e.g. "trigger_worker:" + "audit-watcher").
	Prefix string

	// ResourceKind names what the resource suffix should be — used by
	// the UI to render the grant picker (a workspace-picker for
	// "workspace_name", a worker-picker for "worker_name", etc.).
	// Empty means the scope is boolean (no suffix; grant the literal
	// Prefix string).
	ResourceKind string

	// WildcardAllowed reports whether the grant tool accepts "*" as
	// the resource suffix (e.g. "trigger_worker:*"). False for booleans.
	WildcardAllowed bool

	// Description is the short human-readable hover text shown in the
	// grant picker. Should be one sentence, present-tense.
	Description string

	// Severity is "low" | "medium" | "high" — informs the UI warning
	// level when a user is about to grant this scope to a peer.
	Severity string
}

// Known is the canonical registry. Append-only across releases; do not
// remove entries — a peer might still hold a grant for a deprecated
// scope and the UI needs to render it.
var Known = []ScopeDef{
	{
		Prefix:          "trigger_worker:",
		ResourceKind:    "worker_name",
		WildcardAllowed: true,
		Description:     "Fire one of this daemon's scheduled workers via the mesh.",
		Severity:        "medium",
	},
	{
		Prefix:          "task_offer:",
		ResourceKind:    "workspace_name",
		WildcardAllowed: true,
		Description:     "Suggest tasks into one of this daemon's workspaces (pending until accepted).",
		Severity:        "medium",
	},
	{
		Prefix:          "task_assign:",
		ResourceKind:    "workspace_name",
		WildcardAllowed: true,
		Description:     "Assign tasks directly into one of this daemon's workspaces, skipping the accept step.",
		Severity:        "high",
	},
	{
		Prefix:          "task_sync:",
		ResourceKind:    "workspace_name",
		WildcardAllowed: true,
		Description:     "Replicate the full task state of one of this daemon's workspaces via /mcplexer/task-sync/1.0.0 (read-only cross-peer gossip).",
		Severity:        "medium",
	},
	{
		Prefix:          "mesh.memory_request",
		ResourceKind:    "",
		WildcardAllowed: false,
		Description:     "Request memories this daemon has offered, over the /mcplexer/memory/1.0.0 libp2p protocol.",
		Severity:        "low",
	},
	{
		Prefix:          "mesh.skill_request",
		ResourceKind:    "",
		WildcardAllowed: false,
		Description:     "Request installed .mcskill bundles this daemon has offered, over the /mcplexer/skill/1.0.0 libp2p protocol. Use with mesh__offer_skill / mesh__request_skill.",
		Severity:        "low",
	},
	{
		Prefix:          "mesh.registry_request",
		ResourceKind:    "",
		WildcardAllowed: false,
		Description:     "Request skills (SKILL.md + optional bundle) this daemon has published to its registry, over the /mcplexer/skill-registry/1.0.0 libp2p protocol.",
		Severity:        "low",
	},
	{
		Prefix:          "mesh.attachment_request",
		ResourceKind:    "",
		WildcardAllowed: false,
		Description:     "Fetch task attachments this daemon hosts, over the /mcplexer/attachment/1.0.0 libp2p protocol. Required for cross-peer GET of attachments on tasks the requesting peer already has scope on.",
		Severity:        "medium",
	},
	{
		Prefix:          "mesh.auth_sync",
		ResourceKind:    "",
		WildcardAllowed: false,
		Description:     "Synchronize auth scopes, OAuth provider secrets, OAuth tokens, route rules, and downstream server config with a trusted same-user machine.",
		Severity:        "high",
	},
}

// FindByPrefix returns the ScopeDef matching a literal scope string.
// For colon-prefix scopes ("trigger_worker:foo"), the registry entry
// with Prefix="trigger_worker:" matches. For booleans the full literal
// matches. Returns nil if no entry recognizes the string.
func FindByPrefix(scope string) *ScopeDef {
	for i := range Known {
		d := &Known[i]
		if d.ResourceKind == "" {
			if scope == d.Prefix {
				return d
			}
			continue
		}
		// Colon-prefix shape — require literal prefix match.
		if len(scope) > len(d.Prefix) && scope[:len(d.Prefix)] == d.Prefix {
			return d
		}
	}
	return nil
}
