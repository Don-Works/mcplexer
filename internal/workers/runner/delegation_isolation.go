package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/pathguard"
)

const (
	workerIsolationWorktree = "worktree"
	workerIsolationNone     = "none"
	maxDelegationClaims     = 256
)

type delegationIsolationPolicy struct {
	mode       string
	claims     []string
	reviewOnly bool
}

func (p delegationIsolationPolicy) required() bool {
	return p.mode == workerIsolationWorktree
}

func readDelegationIsolationPolicy(parametersJSON string) (delegationIsolationPolicy, error) {
	if strings.TrimSpace(parametersJSON) == "" {
		return delegationIsolationPolicy{mode: workerIsolationNone}, nil
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parametersJSON), &params); err != nil {
		return delegationIsolationPolicy{}, fmt.Errorf("parse worker parameters for isolation: %w", err)
	}
	raw, ok := params["_mcplexer_delegation"]
	if !ok {
		return delegationIsolationPolicy{mode: workerIsolationNone}, nil
	}
	var meta struct {
		ID              string   `json:"id"`
		Kind            string   `json:"kind"`
		WorkerIsolation string   `json:"worker_isolation"`
		TouchesFiles    []string `json:"touches_files"`
		WorkerMode      string   `json:"worker_mode"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &meta) != nil {
		return delegationIsolationPolicy{}, errors.New("delegation isolation metadata is malformed")
	}
	if strings.TrimSpace(meta.ID) == "" {
		return delegationIsolationPolicy{}, errors.New("delegation isolation metadata has no id")
	}
	if kind := strings.TrimSpace(meta.Kind); kind != "" && kind != "token_preserving_delegation" {
		return delegationIsolationPolicy{}, fmt.Errorf("unsupported delegation metadata kind %q", kind)
	}
	mode := strings.ToLower(strings.TrimSpace(meta.WorkerIsolation))
	if mode == "" {
		mode = workerIsolationWorktree
	}
	if mode != workerIsolationWorktree && mode != workerIsolationNone {
		return delegationIsolationPolicy{}, fmt.Errorf("unsupported delegation worker isolation %q", mode)
	}
	workerMode := strings.ToLower(strings.TrimSpace(meta.WorkerMode))
	if workerMode == "" {
		workerMode = "execute"
	}
	if workerMode != "execute" && workerMode != "review" {
		return delegationIsolationPolicy{}, fmt.Errorf("unsupported delegation worker mode %q", workerMode)
	}
	if len(meta.TouchesFiles) > maxDelegationClaims {
		return delegationIsolationPolicy{}, fmt.Errorf("delegation touches_files max %d", maxDelegationClaims)
	}
	claims := make([]string, 0, len(meta.TouchesFiles))
	seenClaims := make(map[string]struct{}, len(meta.TouchesFiles))
	for _, rawClaim := range meta.TouchesFiles {
		claim := strings.TrimSpace(strings.ReplaceAll(rawClaim, "\\", "/"))
		if claim == "" {
			return delegationIsolationPolicy{}, errors.New("delegation touches_files contains a blank entry")
		}
		if strings.ContainsRune(claim, '\x00') || strings.ContainsAny(claim, "*?[") || strings.Contains(claim, ":") {
			return delegationIsolationPolicy{}, fmt.Errorf("delegation touches_files contains invalid path %q", claim)
		}
		if strings.HasPrefix(claim, "/") || path.IsAbs(claim) {
			return delegationIsolationPolicy{}, fmt.Errorf("delegation touches_files path %q must be relative", claim)
		}
		claim = path.Clean(claim)
		if claim == "." || claim == ".." || strings.HasPrefix(claim, "../") {
			return delegationIsolationPolicy{}, fmt.Errorf("delegation touches_files path %q escapes the workspace", rawClaim)
		}
		if _, seen := seenClaims[claim]; seen {
			continue
		}
		seenClaims[claim] = struct{}{}
		claims = append(claims, claim)
	}
	if mode == workerIsolationNone && len(claims) > 0 {
		return delegationIsolationPolicy{}, errors.New("delegation touches_files requires worker_isolation=worktree")
	}
	return delegationIsolationPolicy{mode: mode, claims: claims, reviewOnly: workerMode == "review"}, nil
}

// DelegationIsolationRequired lets the trusted dispatcher validate that
// persisted worktree metadata and the runtime WorkerRunCtx agree.
func DelegationIsolationRequired(parametersJSON string) (bool, error) {
	policy, err := readDelegationIsolationPolicy(parametersJSON)
	return policy.required(), err
}

func DelegationIsolationReviewOnly(parametersJSON string) (bool, error) {
	policy, err := readDelegationIsolationPolicy(parametersJSON)
	return policy.required() && policy.reviewOnly, err
}

func prepareFilesystemScope(
	lease WorktreeLease, claims []string,
) (pathguard.Scope, error) {
	if lease == nil {
		return pathguard.Scope{}, errors.New("worktree lease is nil")
	}
	scope, err := pathguard.New(lease.RootPath(), lease.WorkspacePath(), claims)
	if err != nil {
		return pathguard.Scope{}, fmt.Errorf("prepare delegated filesystem scope: %w", err)
	}
	return scope, nil
}

func isolatedWorkerPreamble(lease WorktreeLease, scope pathguard.Scope) string {
	var b strings.Builder
	b.WriteString("You are running in an isolated git worktree. Treat it as the only local filesystem root for this run.\n")
	b.WriteString("- branch: " + lease.Branch() + "\n")
	b.WriteString("- working directory: " + scope.WorkingDir() + "\n")
	if claims := scope.Claims(); len(claims) > 0 {
		b.WriteString("- declared write paths are enforced for gateway file tools\n")
	}
	b.WriteString("The trusted runner snapshots all intended repository changes after your final tool call; you do not need access to Git metadata or to create a commit yourself.\n\n")
	return b.String()
}

func cleanupRunWorktree(state *loopState) {
	if state == nil || state.worktree == nil || state.retainWorktree || !state.snapshotComplete || !state.terminalPersisted {
		return
	}
	cleanupWorktreeLease(state.runID, state.isolationRoot, state.worktree)
}

func cleanupWorktreeLease(runID, root string, lease WorktreeLease) {
	if lease == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err = lease.Cleanup(ctx); err == nil {
			return
		}
	}
	if err != nil {
		slog.Error("worker isolated worktree cleanup failed",
			"run_id", runID,
			"worktree_path", root,
			"error", err)
	}
}

func abandonWorktreeLease(runID, root string, lease WorktreeLease) {
	if lease == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := lease.Abandon(ctx); err != nil {
		slog.Error("worker isolated worktree preparation rollback failed",
			"run_id", runID, "worktree_path", root, "error", err)
	}
}
