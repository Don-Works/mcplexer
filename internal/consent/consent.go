// Package consent owns the data shapes + resolver that decide which
// trust tier a peer-to-peer share belongs to and what consent envelope
// must accompany the resulting audit row.
//
// Three tiers (epic 01KSK91Q4W8TNED9MAF0CTRVKC):
//
//	TierSameUser — two machines belonging to the same human. Pairing
//	  auto-grants default scopes; shares are silent. AcceptedBy carries
//	  only Kind="auto_pair".
//	TierSameOrg  — same company / org label, different humans. Every
//	  cross-user share needs an explicit auth_scope grant. AcceptedBy
//	  carries the human's user_id + agent_id + ISO-8601 timestamp.
//	TierCrossOrg — different org. Same explicit-grant requirement +
//	  cross-org boundary check.
//
// The package owns the audit-row shape so internal/audit, internal/mesh,
// the p2p share dispatchers, and the cmd/mcplexer wire adapters can
// agree on field names without circular imports. A Resolver implementation
// lives in cmd/mcplexer where it can talk to the user/peer/settings
// stores; tests can plug in a deterministic fake.
package consent

import (
	"context"
	"encoding/json"
	"time"
)

// Tier is the trust-tier classification.
type Tier string

const (
	// TierSameUser — same human, multiple machines. Auto-grant on pair.
	TierSameUser Tier = "same_user"
	// TierSameOrg — same company, different humans. Explicit grant.
	TierSameOrg Tier = "same_org"
	// TierCrossOrg — different orgs. Explicit grant + boundary check.
	TierCrossOrg Tier = "cross_org"
)

// String returns the wire form. Hand-rolled so callers can use Tier
// directly with json.Marshal without a custom Marshaler.
func (t Tier) String() string { return string(t) }

// AcceptedByKind enumerates how a share was consented to.
type AcceptedByKind string

const (
	// AcceptedKindAutoPair — Tier 1 silent grant. No human in the loop.
	AcceptedKindAutoPair AcceptedByKind = "auto_pair"
	// AcceptedKindHuman — Tier 2/3 explicit human consent.
	AcceptedKindHuman AcceptedByKind = "human"
)

// AcceptedBy is the JSON envelope describing who acknowledged the share.
// Tier 1 rows have Kind=auto_pair and no further fields. Tier 2/3 rows
// have Kind=human plus UserID + AgentID + Timestamp.
type AcceptedBy struct {
	Kind      AcceptedByKind `json:"kind"`
	UserID    string         `json:"user_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	Timestamp time.Time      `json:"timestamp,omitempty"`
}

// MarshalJSON is hand-rolled so the Timestamp serializes as
// RFC-3339 (ISO-8601) and zero-value Timestamp omits cleanly.
func (a AcceptedBy) MarshalJSON() ([]byte, error) {
	out := struct {
		Kind      AcceptedByKind `json:"kind"`
		UserID    string         `json:"user_id,omitempty"`
		AgentID   string         `json:"agent_id,omitempty"`
		Timestamp string         `json:"timestamp,omitempty"`
	}{
		Kind:    a.Kind,
		UserID:  a.UserID,
		AgentID: a.AgentID,
	}
	if !a.Timestamp.IsZero() {
		out.Timestamp = a.Timestamp.UTC().Format(time.RFC3339)
	}
	return json.Marshal(out)
}

// AutoPair returns the canonical Tier 1 envelope.
func AutoPair() AcceptedBy {
	return AcceptedBy{Kind: AcceptedKindAutoPair}
}

// Human returns a Tier 2/3 envelope stamped at now. UserID + AgentID
// must be non-empty for a fully-spec envelope; the caller is responsible
// for that — empty values pass through and the audit row will fail the
// consent_audit assertion downstream.
func Human(userID, agentID string) AcceptedBy {
	return AcceptedBy{
		Kind:      AcceptedKindHuman,
		UserID:    userID,
		AgentID:   agentID,
		Timestamp: time.Now().UTC(),
	}
}

// GrantOrigin references the scope grant that authorized a Tier 2/3
// explicit-grant share. Lets the C2 consent_audit scenario verify
// provenance ("which grant authorized this share?"). PeerID + AgentID
// identify the granter; GrantID is the audit ID of the
// mesh__grant_peer_scope row that recorded the grant (when available)
// or the scope string itself when no grant audit row exists yet.
type GrantOrigin struct {
	PeerID  string `json:"peer_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	GrantID string `json:"grant_id,omitempty"`
}

// IsZero reports whether the GrantOrigin carries no useful fields.
// Callers use this to decide whether to omit the audit-row column.
func (g GrantOrigin) IsZero() bool {
	return g.PeerID == "" && g.AgentID == "" && g.GrantID == ""
}

// Envelope bundles the four cross-boundary fields that decorate an
// audit row. Emit sites build this once via the Resolver and pass it
// into the existing audit-row construction.
type Envelope struct {
	Tier         Tier
	AcceptedBy   AcceptedBy
	GrantOrigin  GrantOrigin
	DenialReason string // populated only on rejection rows
}

// MarshalAcceptedBy returns the JSON form for the AcceptedBy field,
// or nil when the envelope is the zero value (no kind set). Callers
// can pass the result directly to AuditRecord.AcceptedBy.
func (e Envelope) MarshalAcceptedBy() json.RawMessage {
	if e.AcceptedBy.Kind == "" {
		return nil
	}
	raw, err := json.Marshal(e.AcceptedBy)
	if err != nil {
		return nil
	}
	return raw
}

// MarshalGrantOrigin returns the JSON form for the GrantOrigin field,
// or nil when the origin is the zero value.
func (e Envelope) MarshalGrantOrigin() json.RawMessage {
	if e.GrantOrigin.IsZero() {
		return nil
	}
	raw, err := json.Marshal(e.GrantOrigin)
	if err != nil {
		return nil
	}
	return raw
}

// Resolver derives the trust tier + consent envelope for a peer-to-peer
// share. Implementations live outside this package so the consent
// package stays free of store imports (and import cycles). The default
// implementation in cmd/mcplexer consults users + p2p_peers + the
// MCPLEXER_SELF_USER_ID / MCPLEXER_SELF_ORG env vars.
type Resolver interface {
	// TierFor returns the trust tier between the local node and peerID.
	// Falls back to TierCrossOrg on any lookup error — the most-
	// restrictive classification, so a misconfigured node never silently
	// downgrades to TierSameUser.
	TierFor(ctx context.Context, peerID string) Tier

	// AutoPairAccepted reports whether peerID was paired under the
	// auto-pair (Tier 1, same user) flow. True → AcceptedBy{Kind=auto_pair}.
	// False → caller must build a Human envelope.
	AutoPairAccepted(ctx context.Context, peerID string) bool

	// GrantOriginFor returns the GrantOrigin for the most recent scope
	// grant that authorizes shares to peerID for the given scope.
	// Returns a zero GrantOrigin when no grant exists yet — emit sites
	// then omit the audit-row field.
	GrantOriginFor(ctx context.Context, peerID, scope string) GrantOrigin
}

// NopResolver is a Resolver that returns conservative defaults (cross_org,
// no auto-pair, no grant_origin). Used by tests + as a safe fallback when
// the daemon hasn't wired a real resolver yet.
type NopResolver struct{}

// TierFor returns TierCrossOrg unconditionally — the most restrictive
// default so we never silently mark a stranger as same-user.
func (NopResolver) TierFor(context.Context, string) Tier { return TierCrossOrg }

// AutoPairAccepted always returns false on the nop resolver.
func (NopResolver) AutoPairAccepted(context.Context, string) bool { return false }

// GrantOriginFor returns the zero GrantOrigin on the nop resolver.
func (NopResolver) GrantOriginFor(context.Context, string, string) GrantOrigin {
	return GrantOrigin{}
}
