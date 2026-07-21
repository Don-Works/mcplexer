// share_envelope.go — single source of truth for building the
// tier + accepted_by + grant_origin envelope that decorates every
// cross-boundary audit row (skill_share, memory_share, task_share,
// peer-addressed mesh__send).
//
// Centralised so the four adapters can't drift on consent semantics —
// any future refinement (per-grant agent ids, org-aware tier check)
// lands here once and propagates.
package main

import (
	"context"
	"strings"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/store"
)

// shareEnvelope builds the consent.Envelope for one cross-boundary
// audit row. Inputs:
//
//	resolver — production resolver or NopResolver in tests / pre-bootstrap
//	selfUser — local user.is_self row (nil → AcceptedBy.UserID stays "")
//	peerID   — the other side of the share
//	scope    — the named scope this share rides on (used for grant_origin)
//	status   — "ok", "success", "error", "denied", "pending", ...
//	errMsg   — wrapped error string; mined for a stable DenialReason
//
// Outputs:
//
//	Tier              — resolver verdict; never empty (defaults to cross_org)
//	AcceptedBy        — auto_pair on Tier 1, human on Tier 2/3
//	GrantOrigin       — populated on Tier 2/3 success rows; zero on Tier 1
//	                    and on denial rows (where no grant was in play)
//	DenialReason      — derived from errMsg when status indicates rejection
func shareEnvelope(
	ctx context.Context,
	resolver consent.Resolver,
	selfUser *store.User,
	peerID, scope, status, errMsg string,
) consent.Envelope {
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	tier := resolver.TierFor(ctx, peerID)

	env := consent.Envelope{Tier: tier}

	switch tier {
	case consent.TierSameUser:
		env.AcceptedBy = consent.AutoPair()
	default:
		// Tier 2/3 — human consent envelope. The user_id is the LOCAL
		// user (the human operating this daemon, who is the side that
		// owns the audit record). AgentID is the agent_id of the
		// session that initiated the share — for now we use the
		// local user_id as a placeholder until the gateway threads
		// session-scoped agent_id into the share adapters.
		userID := ""
		agentID := ""
		if selfUser != nil {
			userID = selfUser.UserID
			agentID = selfUser.UserID
		}
		env.AcceptedBy = consent.Human(userID, agentID)
	}

	if isShareSuccess(status) && tier != consent.TierSameUser {
		env.GrantOrigin = resolver.GrantOriginFor(ctx, peerID, scope)
	}

	if isShareRejection(status, errMsg) {
		env.DenialReason = denialReasonFromError(tier, errMsg)
	}

	return env
}

// isShareSuccess reports whether the status string indicates a success-
// shaped transition. Strings emitted by the various share dispatchers
// include "ok", "success", "pending" (offer-received, awaiting decision).
// Treat pending as success-shaped for envelope purposes — it's a
// successful state transition that needs the grant trail.
func isShareSuccess(status string) bool {
	switch strings.ToLower(status) {
	case "ok", "success", "pending":
		return true
	}
	return false
}

// isShareRejection reports whether the row records a deny / error
// outcome that should populate DenialReason.
func isShareRejection(status, errMsg string) bool {
	s := strings.ToLower(status)
	if s == "denied" || s == "error" || s == "rejected" {
		return true
	}
	if errMsg != "" && strings.Contains(strings.ToLower(errMsg), "denied") {
		return true
	}
	return false
}

// denialReasonFromError maps wire-level error messages to a stable
// short code that filters cleanly in the dashboard. Returns "" when no
// pattern matches — the caller then leaves DenialReason empty and the
// dashboard falls back to error_message.
//
// Coordinated with the parallel BUG-DENIAL agent (commit 812dd46 on
// main): the codes below align with internal/scopes/denial.go's
// DenialCode vocabulary (no_scope, scope_revoked, scope_out_of_band,
// cross_org_boundary). Wire-only codes (not_paired, not_installed,
// not_found, too_large) extend that vocabulary with audit-row-specific
// reasons that don't apply to the REST denial envelope. Post-merge a
// follow-up will import scopes.DenialCode and replace the literals,
// once that package is reachable from this worktree.
func denialReasonFromError(tier consent.Tier, errMsg string) string {
	lower := strings.ToLower(errMsg)
	switch {
	case strings.Contains(lower, "not paired"):
		return "not_paired"
	case strings.Contains(lower, "revoked"):
		return "scope_revoked"
	case strings.Contains(lower, "scope") && strings.Contains(lower, "required"):
		if tier == consent.TierCrossOrg {
			return "cross_org_boundary"
		}
		return "no_scope"
	case strings.Contains(lower, "not installed") ||
		strings.Contains(lower, "not_installed"):
		return "not_installed"
	case strings.Contains(lower, "not found") ||
		strings.Contains(lower, "not_found"):
		return "not_found"
	case strings.Contains(lower, "too large") ||
		strings.Contains(lower, "too_large"):
		return "too_large"
	case strings.Contains(lower, "denied"):
		// Generic fallback — at least signals "this is a deny, not a
		// crash". Refined as BUG-DENIAL lands its vocabulary.
		if tier == consent.TierCrossOrg {
			return "cross_org_boundary"
		}
		return "no_scope"
	}
	return ""
}
