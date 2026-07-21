package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// FileClaimStore is the subset of store operations needed for delegation
// file-claim coordination (claim overlap detection, auto-claim, release).
// *sqlite.DB satisfies it structurally.
type FileClaimStore interface {
	InsertFileClaim(ctx context.Context, claim *store.FileClaim) error
	ListFileClaims(ctx context.Context, filter store.FileClaimFilter) ([]store.FileClaim, error)
	ReleaseFileClaim(ctx context.Context, claimID string, releasedAt time.Time) error
}

// delegationClaimID is deterministic so release needs no lookup and a
// duplicate insert for the same delegation fails loudly on the PK.
func delegationClaimID(delegationID string) string { return "fc-" + delegationID }

// delegationClaimRepo resolves a NON-EMPTY scope key for a delegation's
// claims. It prefers the workspace root path (so claims interoperate with
// git-repo-keyed mesh claims), but falls back to a synthetic
// "ws:<workspace-id>" key when the root path is empty (P2P-shared
// workspaces store RootPath="").
//
// The fallback is load-bearing: FileClaimFilter treats Repo=="" as "no
// filter" (match every repo), so if a claim were STORED with repo="" and
// later CHECKED with a resolved repo, the check would never see it
// (false negative), while an unscoped check would see every repo's
// claims (false positive). Guaranteeing a non-empty, deterministic key
// on both the claim and check paths removes that asymmetry. Returns ""
// only when there is no workspace id at all, in which case the caller
// skips coordination entirely.
func (s *Service) delegationClaimRepo(ctx context.Context, workspaceID string) string {
	if workspaceID == "" {
		return ""
	}
	if s.workspaces != nil {
		if workspaces, err := s.workspaces.ListWorkspaces(ctx); err == nil {
			for _, ws := range workspaces {
				if ws.ID == workspaceID && ws.RootPath != "" {
					return ws.RootPath
				}
			}
		}
	}
	return "ws:" + workspaceID
}

// checkFileClaimOverlap returns non-blocking warnings for each file in
// paths that is already actively claimed by someone else in the same
// repo. Matching is exact-string on both sides, mirroring the task
// ledger's touches_files contract (no glob expansion — delegation
// touches_files are validated glob-free at normalize time).
func (s *Service) checkFileClaimOverlap(ctx context.Context, paths []string, delegationID, repo string) []string {
	if s.fileClaimStore == nil || len(paths) == 0 {
		return nil
	}
	active, err := s.fileClaimStore.ListFileClaims(ctx, store.FileClaimFilter{
		Repo:       repo,
		ActiveOnly: true,
		Now:        s.clock.Now().UTC(),
	})
	if err != nil {
		slog.Warn("delegation: file claim overlap check failed", "error", err)
		return nil
	}
	pathSet := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		pathSet[strings.TrimSpace(p)] = struct{}{}
	}
	seen := make(map[string]bool)
	var warnings []string
	ownID := delegationClaimID(delegationID)
	for _, claim := range active {
		if claim.ClaimID == ownID {
			continue
		}
		holder := claim.ClaimerDisplayName
		if holder == "" {
			holder = claim.ClaimerUserID
		}
		for _, claimed := range claim.Paths {
			if _, ok := pathSet[claimed]; ok && !seen[claimed] {
				seen[claimed] = true
				warnings = append(warnings, fmt.Sprintf(
					"file %q already claimed by %s (intent: %s) - potential duplicate work",
					claimed, holder, claim.Intent))
			}
		}
	}
	return warnings
}

// claimDelegationFiles records an advisory claim over the delegation's
// touches_files, scoped to the workspace repo, expiring at the
// delegation's wall-clock budget plus slack so an unreleased claim
// self-heals quickly. Best-effort: failures are logged, never propagated.
func (s *Service) claimDelegationFiles(ctx context.Context, delegationID string, in *DelegationInput, repo string) {
	if s.fileClaimStore == nil || len(in.TouchesFiles) == 0 {
		return
	}
	now := s.clock.Now().UTC()
	ttl := time.Duration(in.MaxWallClockSeconds)*time.Second + 10*time.Minute
	intent := in.Objective
	if len(intent) > 200 {
		intent = intent[:200]
	}
	claim := &store.FileClaim{
		ClaimID:            delegationClaimID(delegationID),
		ClaimerDisplayName: "delegation " + delegationID,
		Repo:               repo,
		Paths:              append([]string(nil), in.TouchesFiles...),
		Intent:             intent,
		ClaimedAt:          now,
		ExpiresAt:          now.Add(ttl),
	}
	if err := s.fileClaimStore.InsertFileClaim(ctx, claim); err != nil {
		slog.Warn("delegation: file claim insert failed", "delegation_id", delegationID, "error", err)
	}
}

// releaseDelegationFileClaims releases the delegation's claim by its
// deterministic ID. Best-effort and idempotent: an already-released or
// never-created claim is not an error worth surfacing.
func (s *Service) releaseDelegationFileClaims(ctx context.Context, delegationID string) {
	if s.fileClaimStore == nil {
		return
	}
	err := s.fileClaimStore.ReleaseFileClaim(ctx, delegationClaimID(delegationID), s.clock.Now().UTC())
	if err != nil && err != store.ErrNotFound {
		slog.Warn("delegation: release claim failed", "delegation_id", delegationID, "error", err)
	}
}

// SetFileClaimStore injects the file claim store dependency.
func (s *Service) SetFileClaimStore(fcs FileClaimStore) {
	s.fileClaimStore = fcs
}
