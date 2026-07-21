package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ErrNotConfirmed signals the user declined the adoption prompt — the
// installer wrote nothing.
var ErrNotConfirmed = errors.New("githook: install not confirmed")

// gitHookClientID is the InstalledClient.ID we record receipts under
// so the existing receipt-listing UI/CLI surfaces git hook installs
// alongside other reversible mutations.
const gitHookClientID = "git_hook"

// hookReverseData is the JSON blob written into InstallReceipt.ReverseData
// for a git hook write. It records the originating jobID so the audit
// trail explains *why* the hook was installed without re-reading the
// backup.
type hookReverseData struct {
	JobID    string `json:"job_id"`
	HookName string `json:"hook_name"`
	RepoRoot string `json:"repo_root"`
}

// ReceiptStore is the narrow store surface GitHookInstaller needs. It is
// satisfied by sqlite.DB and by the install-package fake without modification.
type ReceiptStore interface {
	CreateInstallReceipt(ctx context.Context, r *store.InstallReceipt) error
	ListInstallReceipts(ctx context.Context, clientID string, includeReversed bool) ([]store.InstallReceipt, error)
	MarkReceiptReversed(ctx context.Context, id string, reverseError string) error
}

// GitHookInstaller writes wrapper scripts into a repo's .git/hooks/
// directory that exec back through `mcplexer run-job <id>`. Hooks are
// backed up to ~/.mcplexer/backups/githooks-<repo-hash>/ before any
// overwrite; an InstallReceipt records the swap so uninstall reverses
// cleanly.
//
// Adoption is always-confirm: the public API takes a Confirm callback
// the wizard wires to interactive y/n.
type GitHookInstaller struct {
	home      string
	backupDir string
	store     ReceiptStore
	confirm   func(prompt string) bool
}

// NewGitHookInstaller anchors a hook installer at `home`. confirm may
// be nil — in that case install always proceeds (used by automated /
// already-confirmed callers).
func NewGitHookInstaller(
	home string, s ReceiptStore, confirm func(prompt string) bool,
) (*GitHookInstaller, error) {
	if home == "" {
		return nil, errors.New("githook: home required")
	}
	if s == nil {
		return nil, errors.New("githook: store required")
	}
	return &GitHookInstaller{
		home:      home,
		backupDir: filepath.Join(home, ".mcplexer", "backups"),
		store:     s,
		confirm:   confirm,
	}, nil
}

// hookContent renders the wrapper script body for the given jobID. The
// script execs the mcplexer CLI so the existing daemon-or-direct
// fallback in RunJob applies.
func hookContent(jobID string) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
# Installed by mcplexer; revert with 'mcplexer__githook_uninstall <repo> <hook>'.
exec mcplexer run-job %s "$@"
`, jobID)
}

// InstallHook writes the wrapper for hookName ("pre-commit", "pre-push",
// ...) into <repoRoot>/.git/hooks/. Returns ErrNotConfirmed if the
// confirm callback returns false.
func (g *GitHookInstaller) InstallHook(
	ctx context.Context, repoRoot, hookName, jobID string,
) (*store.InstallReceipt, error) {
	if err := validateInstallArgs(repoRoot, hookName, jobID); err != nil {
		return nil, err
	}
	hookPath := filepath.Join(repoRoot, ".git", "hooks", hookName)
	prompt := fmt.Sprintf(
		"Install mcplexer wrapper for hook %q in repo %s -> job %s?",
		hookName, repoRoot, jobID,
	)
	if g.confirm != nil && !g.confirm(prompt) {
		return nil, ErrNotConfirmed
	}
	backupPath, err := g.backupIfPresent(repoRoot, hookPath, hookName)
	if err != nil {
		return nil, fmt.Errorf("githook: backup: %w", err)
	}
	if err := writeExecAtomic(hookPath, []byte(hookContent(jobID)), 0o755); err != nil {
		return nil, fmt.Errorf("githook: write: %w", err)
	}
	return g.recordReceipt(ctx, hookPath, backupPath, jobID, hookName, repoRoot)
}

// validateInstallArgs centralises the cheap checks on caller input so
// InstallHook stays under the line cap.
func validateInstallArgs(repoRoot, hookName, jobID string) error {
	if repoRoot == "" {
		return errors.New("githook: repoRoot required")
	}
	if hookName == "" {
		return errors.New("githook: hookName required")
	}
	if jobID == "" {
		return errors.New("githook: jobID required")
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		return fmt.Errorf("githook: %s is not a git repo: %w", repoRoot, err)
	}
	return nil
}

// backupIfPresent copies an existing hook file aside before overwrite.
// Returns the backup path (empty string when no prior file existed).
func (g *GitHookInstaller) backupIfPresent(repoRoot, hookPath, hookName string) (string, error) {
	if _, err := os.Stat(hookPath); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	dir := filepath.Join(g.backupDir, "githooks-"+repoHash(repoRoot))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("2006-01-02T150405Z")
	dst := filepath.Join(dir, fmt.Sprintf("%s-%s", hookName, ts))
	in, err := os.ReadFile(hookPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, in, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

// recordReceipt persists a write_file receipt for a successful install.
func (g *GitHookInstaller) recordReceipt(
	ctx context.Context, target, backupPath, jobID, hookName, repoRoot string,
) (*store.InstallReceipt, error) {
	rev, err := json.Marshal(hookReverseData{
		JobID: jobID, HookName: hookName, RepoRoot: repoRoot,
	})
	if err != nil {
		return nil, err
	}
	r := &store.InstallReceipt{
		ID:          newReceiptID(),
		ClientID:    gitHookClientID,
		Action:      "write_file",
		TargetPath:  target,
		BackupPath:  backupPath,
		ReverseData: string(rev),
		AppliedAt:   time.Now().UTC(),
	}
	if err := g.store.CreateInstallReceipt(ctx, r); err != nil {
		return nil, fmt.Errorf("githook: receipt: %w", err)
	}
	return r, nil
}

// UninstallHook consumes the latest hook Receipt and restores the prior
// content (or deletes the wrapper if there was no prior). Idempotent.
func (g *GitHookInstaller) UninstallHook(
	ctx context.Context, repoRoot, hookName string,
) error {
	if repoRoot == "" || hookName == "" {
		return errors.New("githook: repoRoot and hookName required")
	}
	target := filepath.Join(repoRoot, ".git", "hooks", hookName)
	receipts, err := g.store.ListInstallReceipts(ctx, gitHookClientID, false)
	if err != nil {
		return fmt.Errorf("githook: list receipts: %w", err)
	}
	r := latestWriteFile(receipts, target)
	if r == nil {
		return nil
	}
	if err := reverseWriteFile(*r); err != nil {
		_ = g.store.MarkReceiptReversed(ctx, r.ID, err.Error())
		return err
	}
	return g.store.MarkReceiptReversed(ctx, r.ID, "")
}

// ListInstalled returns the set of git hooks mcplexer has installed in
// the given repo (one entry per kind it's installed). The list is
// derived from the receipt log filtered to un-reversed write_file
// receipts under the repo's .git/hooks/ directory.
func (g *GitHookInstaller) ListInstalled(
	ctx context.Context, repoRoot string,
) ([]string, error) {
	if repoRoot == "" {
		return nil, errors.New("githook: repoRoot required")
	}
	receipts, err := g.store.ListInstallReceipts(ctx, gitHookClientID, false)
	if err != nil {
		return nil, fmt.Errorf("githook: list receipts: %w", err)
	}
	prefix := filepath.Join(repoRoot, ".git", "hooks") + string(filepath.Separator)
	seen := map[string]struct{}{}
	for _, r := range receipts {
		if r.Action != "write_file" || r.ReversedAt != nil {
			continue
		}
		if r.TargetPath != "" && hasPrefix(r.TargetPath, prefix) {
			seen[filepath.Base(r.TargetPath)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}
