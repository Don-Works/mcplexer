// Package scopes is the leaf type for cross-peer scope denial vocabulary.
//
// It exists so any subsystem (REST handlers, p2p wire envelopes, audit
// rows, task offer rejection records) can stamp a SAME typed code on a
// rejection without dragging api/p2p/audit imports into each other.
//
// The vocabulary is the answer to "why was this cross-peer attempt
// rejected?" — the four canonical cases the bug description (JTAC65)
// enumerates:
//
//   - DenialNoScope          — never granted
//   - DenialScopeRevoked     — was granted, then revoked
//   - DenialScopeOutOfBand   — scope ID doesn't apply to this peer
//   - DenialCrossOrgBoundary — denied because of org-pair boundary (Tier 3)
//
// Callers MUST pick exactly one code per deny event. If none fits, leave
// the existing untyped 403/error in place rather than inventing a fifth
// code — back-compat is "no `denial` field, generic error". New sites
// pass a code.
package scopes

// DenialCode is the typed, machine-readable rejection reason that
// accompanies a forbidden response. The string value is the on-the-wire
// JSON token; callers MUST use the consts below rather than string
// literals so the compiler catches typos.
type DenialCode string

// Canonical denial codes. Append-only across releases — clients may
// switch on these strings, so removing one is a wire-break.
const (
	// DenialNoScope — the requested action requires a scope this peer
	// has never been granted. The natural fix is for the local operator
	// to GrantPeerScope(...).
	DenialNoScope DenialCode = "no_scope"

	// DenialScopeRevoked — the scope was granted at some point in the
	// past but has since been revoked (RevokePeer or RevokePeerScope).
	// Distinguished from DenialNoScope so the calling agent can tell
	// "ask for re-grant" vs "ask for first-time grant".
	DenialScopeRevoked DenialCode = "scope_revoked"

	// DenialScopeOutOfBand — the scope string itself doesn't apply to
	// this peer (e.g. a workspace-scoped grant whose workspace was
	// renamed/deleted, or a per-worker scope for a worker that no
	// longer exists). Different from no_scope: there is no path to
	// "just grant it" — the scope ID itself needs to be re-derived.
	DenialScopeOutOfBand DenialCode = "scope_out_of_band"

	// DenialCrossOrgBoundary — the peer's org and the resource's org
	// don't match under the Tier-3 cross-org policy. Even a granted
	// scope can't bypass this boundary; the operator has to either move
	// the resource or expand the org pair binding.
	DenialCrossOrgBoundary DenialCode = "cross_org_boundary"
)

// String returns the wire token (so DenialCode satisfies fmt.Stringer
// without an explicit cast).
func (c DenialCode) String() string { return string(c) }

// Valid reports whether c is one of the known canonical codes.
// Useful for inbound validation when a deny envelope is decoded off
// the wire and the receiver wants to defensively reject unknown codes.
func (c DenialCode) Valid() bool {
	switch c {
	case DenialNoScope,
		DenialScopeRevoked,
		DenialScopeOutOfBand,
		DenialCrossOrgBoundary:
		return true
	}
	return false
}

// Denial is the structured deny body. Code is required; the other
// fields are best-effort hints that help the caller understand which
// scope on which peer failed.
//
// Marshalled with omitempty on the optional fields so a minimal deny
// ({"code":"scope_revoked"}) stays terse on the wire.
type Denial struct {
	Code    DenialCode `json:"code"`
	Scope   string     `json:"scope,omitempty"`
	Peer    string     `json:"peer,omitempty"`
	Message string     `json:"message,omitempty"`
}

// New builds a Denial with the four common fields. Returns a value
// (not a pointer) so callers can embed it inline in response bodies
// without nil checks.
func New(code DenialCode, scope, peer string) Denial {
	return Denial{Code: code, Scope: scope, Peer: peer}
}

// WithMessage returns a copy of d with the human-readable message
// field populated. Kept as a chained builder so the common no-message
// case stays one line.
func (d Denial) WithMessage(msg string) Denial {
	d.Message = msg
	return d
}
