package admin

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const maxDelegationTouchedFiles = 256

const (
	workerIsolationWorktree = "worktree"
	workerIsolationNone     = "none"
)

func normalizeDelegationIsolationInput(in *DelegationInput) error {
	if in == nil {
		return errors.New("delegation input required")
	}
	in.WorkerIsolation = strings.ToLower(strings.TrimSpace(in.WorkerIsolation))
	if in.WorkerIsolation == "" {
		in.WorkerIsolation = workerIsolationWorktree
	}
	if in.WorkerIsolation != workerIsolationWorktree && in.WorkerIsolation != workerIsolationNone {
		return errors.New("worker_isolation must be worktree or none")
	}
	if len(in.TouchesFiles) > maxDelegationTouchedFiles {
		return fmt.Errorf("touches_files max %d", maxDelegationTouchedFiles)
	}
	seen := make(map[string]struct{}, len(in.TouchesFiles))
	normalized := make([]string, 0, len(in.TouchesFiles))
	for _, raw := range in.TouchesFiles {
		claim := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if claim == "" {
			return errors.New("touches_files entries must not be blank")
		}
		if strings.ContainsRune(claim, '\x00') || strings.ContainsAny(claim, "*?[") || strings.Contains(claim, ":") {
			return fmt.Errorf("touches_files path %q must be a concrete repository-relative path", raw)
		}
		if strings.HasPrefix(claim, "/") || path.IsAbs(claim) {
			return fmt.Errorf("touches_files path %q must be relative", raw)
		}
		claim = path.Clean(claim)
		if claim == "." || claim == ".." || strings.HasPrefix(claim, "../") {
			return fmt.Errorf("touches_files path %q escapes the workspace", raw)
		}
		if _, ok := seen[claim]; ok {
			continue
		}
		seen[claim] = struct{}{}
		normalized = append(normalized, claim)
	}
	if in.WorkerIsolation == workerIsolationNone && len(normalized) > 0 {
		return errors.New("touches_files requires worker_isolation=worktree")
	}
	in.TouchesFiles = normalized
	return nil
}
