package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// handleSkillProposeRefinement implements skill__propose_refinement.
//
// Insertion path:
//  1. Validate inputs (skill / friction / suggested_change / rationale).
//  2. Resolve the skill's current registry version so the proposal is
//     pinned to a concrete target (otherwise reviewers can't tell
//     which version this would supersede).
//  3. Record the proposal as `pending`.
//  4. Run the quorum check: count similar proposals across the mesh
//     (fuzzy-matched friction substring). When the count crosses the
//     threshold, transition the freshly-inserted proposal to
//     `candidate` AND broadcast a mesh `finding` so other agents +
//     the dashboard know the inbox has fresh signal.
//
// Returns {proposal_id, status, quorum_count, candidate}.
func (h *handler) handleSkillProposeRefinement(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult(
			"Skills registry is not enabled — cannot record refinement proposals."), nil
	}
	var args struct {
		Skill           string `json:"skill"`
		Friction        string `json:"friction"`
		SuggestedChange string `json:"suggested_change"`
		Rationale       string `json:"rationale"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if err := validateProposeArgs(args.Skill, args.Friction, args.SuggestedChange, args.Rationale); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}

	wsID := h.currentWorkspaceID(ctx)
	if wsID == "" {
		// Refinement is per-workspace by design — proposals scoped to a
		// workspace let reviewers triage their own team's signal without
		// drowning in everyone else's gripes. Fail loudly.
		return marshalErrorResult(
			"skill__propose_refinement: session is not rooted in a workspace; cannot record proposal. " +
				"Open a CWD inside a registered workspace and try again."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}

	version, rpcErr := h.resolveSkillVersion(ctx, args.Skill)
	if rpcErr != nil {
		return nil, rpcErr
	}

	proposal := &store.SkillRefinementProposal{
		SkillName:           args.Skill,
		SkillVersion:        version,
		Friction:            args.Friction,
		SuggestedChange:     args.SuggestedChange,
		Rationale:           args.Rationale,
		ProposedBySessionID: h.sessions.sessionID(),
		ProposedByPeerID:    h.selfPeerID(),
		WorkspaceID:         wsID,
	}
	if err := h.store.RecordRefinementProposal(ctx, proposal); err != nil {
		return nil, &RPCError{Code: CodeInternalError,
			Message: fmt.Sprintf("record refinement proposal: %v", err)}
	}

	// Quorum check — count similar proposals across ALL workspaces +
	// peers, not just this one. Cross-team signal is the whole point
	// of the mesh-quorum gate; a single team can flood without
	// triggering promotion (good), but two teams hitting the same
	// wall promote fast (also good).
	count, candidate := h.applyQuorum(ctx, proposal)

	resp := map[string]any{
		"proposal_id":   proposal.ID,
		"status":        proposal.Status,
		"quorum_count":  count,
		"candidate":     candidate,
		"skill_version": version,
	}
	out, _ := json.Marshal(resp)
	return marshalToolResult(string(out)), nil
}

// validateProposeArgs trims + checks the four required fields. Pulled
// out so handleSkillProposeRefinement stays under the 50-line cap.
func validateProposeArgs(skill, friction, suggested, rationale string) error {
	if strings.TrimSpace(skill) == "" {
		return errors.New("skill is required")
	}
	if strings.TrimSpace(friction) == "" {
		return errors.New("friction is required")
	}
	if strings.TrimSpace(suggested) == "" {
		return errors.New("suggested_change is required")
	}
	if strings.TrimSpace(rationale) == "" {
		return errors.New("rationale is required")
	}
	return nil
}

// resolveSkillVersion fetches the latest registry version for the
// skill so the proposal is pinned. Returns ErrNotFound-style RPCError
// when the skill doesn't exist.
func (h *handler) resolveSkillVersion(ctx context.Context, name string) (int, *RPCError) {
	entry, err := h.skillRegistry.Get(ctx, h.sessionScope(ctx), name, skillregistry.VersionRef{Latest: true})
	if errors.Is(err, store.ErrNotFound) {
		return 0, &RPCError{Code: CodeInvalidParams,
			Message: fmt.Sprintf("skill %q not found in registry", name)}
	}
	if err != nil {
		return 0, &RPCError{Code: CodeInternalError,
			Message: fmt.Sprintf("resolve skill version: %v", err)}
	}
	return entry.Version, nil
}

// applyQuorum counts similar proposals + promotes the freshly-inserted
// one to `candidate` when the threshold is crossed. Broadcasts a mesh
// finding on promotion so other agents + the dashboard notice fresh
// inbox signal. Returns (count, didPromote). All side effects are
// best-effort — promotion failure does not undo the inserted proposal.
func (h *handler) applyQuorum(
	ctx context.Context, proposal *store.SkillRefinementProposal,
) (int, bool) {
	pattern := skillregistry.SimilarFrictionPattern(proposal.Friction)
	count, err := h.store.CountSimilarProposals(ctx, proposal.SkillName, pattern)
	if err != nil {
		slog.Warn("propose_refinement: count similar failed",
			"skill", proposal.SkillName, "err", err)
		return 0, false
	}
	if !skillregistry.QuorumReached(count, 0) {
		return count, false
	}
	candidateStatus := store.RefinementStatusCandidate
	now := proposal.CreatedAt
	if err := h.store.UpdateRefinementProposal(ctx, proposal.ID, store.RefinementProposalPatch{
		Status:      &candidateStatus,
		CandidateAt: &now,
	}); err != nil {
		slog.Warn("propose_refinement: promote to candidate failed",
			"id", proposal.ID, "err", err)
		return count, false
	}
	proposal.Status = candidateStatus
	proposal.CandidateAt = &now
	h.broadcastQuorumFinding(ctx, proposal, count)
	return count, true
}

// broadcastQuorumFinding emits a mesh `finding` so peers + dashboard
// know the inbox has a fresh candidate. No-op when mesh isn't wired.
func (h *handler) broadcastQuorumFinding(
	ctx context.Context, proposal *store.SkillRefinementProposal, count int,
) {
	if h.mesh == nil {
		return
	}
	content := fmt.Sprintf(
		"Skill refinement quorum reached for %s@%d (count=%d). Friction: %q. (Dashboard review UI pending; see skills-coherence task.) Proposal: %s",
		proposal.SkillName, proposal.SkillVersion, count,
		truncForMesh(proposal.Friction, 120), proposal.ID,
	)
	meta := h.sessionMeshMeta(ctx)
	if _, err := h.mesh.Send(ctx, meta, mesh.SendRequest{
		Kind:     "finding",
		Content:  content,
		Tags:     "skill-refinement,quorum,candidate",
		Audience: "*",
	}); err != nil {
		slog.Warn("propose_refinement: mesh broadcast failed",
			"id", proposal.ID, "err", err)
	}
}

func truncForMesh(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// selfPeerID returns the local libp2p peer ID when mesh is wired,
// else empty. Empty = local-only deployment; refinement proposals
// from such hosts won't carry cross-peer attribution.
func (h *handler) selfPeerID() string {
	if h.mesh == nil {
		return ""
	}
	return h.mesh.SelfPeerID()
}

// handleSkillAdoptRefinement implements skill__adopt_refinement.
// It takes a proposal (candidate or promoted), publishes its
// suggested_change as a new registry version (parent_version set),
// then marks the proposal applied. Requires workspace write on the
// proposal's workspace. This closes the refinement loop from agent
// proposal to published registry version.
func (h *handler) handleSkillAdoptRefinement(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.skillRegistry == nil {
		return marshalErrorResult(
			"Skills registry is not enabled — cannot adopt refinements."), nil
	}
	var args struct {
		ProposalID string `json:"proposal_id"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if strings.TrimSpace(args.ProposalID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "proposal_id is required"}
	}

	proposal, err := h.store.GetRefinementProposal(ctx, args.ProposalID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult("refinement proposal not found"), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("load proposal: %v", err)}
	}

	// Only adopt from states that indicate review has happened.
	if proposal.Status != store.RefinementStatusCandidate && proposal.Status != store.RefinementStatusPromoted {
		return marshalErrorResult(
			fmt.Sprintf("proposal status %q is not adoptable (must be candidate or promoted)", proposal.Status)), nil
	}

	// Must have write on the proposal's workspace (the scope in which it was filed).
	if rpc := h.requireWorkspaceWrite(ctx, proposal.WorkspaceID); rpc != nil {
		return nil, rpc
	}

	// Publish the suggested_change as the new body. suggested_change is
	// the "proposed ... rewrite" from the proposer; treat as replacement
	// body for the child version (consistent with "rewrite" guidance).
	parent := proposal.SkillVersion
	res, pubErr := h.skillRegistry.Publish(ctx, skillregistry.PublishOptions{
		Name:             proposal.SkillName,
		Body:             proposal.SuggestedChange,
		ParentVersion:    &parent,
		Author:           "refinement-adopt",
		CreatedByAgentID: h.sessions.sessionID(),
		WorkspaceID:      &proposal.WorkspaceID,
	})
	if pubErr != nil {
		return marshalErrorResult(fmt.Sprintf("adopt publish failed: %v", pubErr)), nil
	}

	// Mark the proposal applied (terminal). Update stamps ResolvedAt.
	applied := store.RefinementStatusApplied
	note := fmt.Sprintf("adopted as %s@%d (content_hash=%s)", res.Name, res.Version, res.ContentHash)
	if err := h.store.UpdateRefinementProposal(ctx, proposal.ID, store.RefinementProposalPatch{
		Status:         &applied,
		ResolutionNote: &note,
	}); err != nil {
		// Publish succeeded; proposal update is best-effort for audit.
		slog.Warn("adopt_refinement: publish ok but failed to mark proposal applied",
			"proposal", proposal.ID, "new_version", res.Version, "err", err)
	} else {
		proposal.Status = applied
	}

	resp := map[string]any{
		"proposal_id":     proposal.ID,
		"status":          proposal.Status,
		"adopted_name":    res.Name,
		"adopted_version": res.Version,
		"content_hash":    res.ContentHash,
		"action":          res.Action,
	}
	out, _ := json.Marshal(resp)
	return marshalToolResult(string(out)), nil
}
