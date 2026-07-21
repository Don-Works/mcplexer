package triggers

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeReceiptStore is an in-memory ReceiptStore used by the githook tests.
type fakeReceiptStore struct {
	receipts []store.InstallReceipt
}

func (f *fakeReceiptStore) CreateInstallReceipt(_ context.Context, r *store.InstallReceipt) error {
	cp := *r
	if cp.AppliedAt.IsZero() {
		cp.AppliedAt = time.Now().UTC()
	}
	// Mirror sqlite DESC-by-applied_at ordering.
	f.receipts = append([]store.InstallReceipt{cp}, f.receipts...)
	return nil
}

func (f *fakeReceiptStore) ListInstallReceipts(
	_ context.Context, clientID string, includeReversed bool,
) ([]store.InstallReceipt, error) {
	var out []store.InstallReceipt
	for _, r := range f.receipts {
		if clientID != "" && r.ClientID != clientID {
			continue
		}
		if !includeReversed && r.ReversedAt != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeReceiptStore) MarkReceiptReversed(_ context.Context, id, reverseError string) error {
	now := time.Now().UTC()
	for i := range f.receipts {
		if f.receipts[i].ID == id {
			f.receipts[i].ReversedAt = &now
			f.receipts[i].ReverseError = reverseError
			return nil
		}
	}
	return errors.New("receipt not found")
}

// gitInit makes a tmp dir into a minimal git repo (just enough to
// satisfy the .git/hooks/ presence check).
func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func newTestGitHook(t *testing.T, confirm func(string) bool) (*GitHookInstaller, *fakeReceiptStore, string) {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	gitInit(t, repo)
	fs := &fakeReceiptStore{}
	g, err := NewGitHookInstaller(home, fs, confirm)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return g, fs, repo
}

func TestInstallHookFreshRepo(t *testing.T) {
	g, fs, repo := newTestGitHook(t, func(string) bool { return true })
	r, err := g.InstallHook(context.Background(), repo, "pre-commit", "job1")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if r == nil || r.Action != "write_file" {
		t.Fatalf("expected write_file receipt, got %+v", r)
	}
	if r.BackupPath != "" {
		t.Errorf("expected empty BackupPath on fresh install, got %q", r.BackupPath)
	}
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	if !strings.Contains(string(data), "mcplexer run-job job1") {
		t.Errorf("hook missing run-job line: %s", data)
	}
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("hook not executable: %o", info.Mode())
	}
	if len(fs.receipts) != 1 {
		t.Errorf("expected 1 receipt, got %d", len(fs.receipts))
	}
}

func TestListInstalled(t *testing.T) {
	g, _, repo := newTestGitHook(t, nil)
	if _, err := g.InstallHook(context.Background(), repo, "pre-commit", "j1"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.InstallHook(context.Background(), repo, "pre-push", "j2"); err != nil {
		t.Fatal(err)
	}
	got, err := g.ListInstalled(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "pre-commit" || got[1] != "pre-push" {
		t.Errorf("ListInstalled = %v", got)
	}
}

func TestUninstallHookFreshRepoRemovesFile(t *testing.T) {
	g, _, repo := newTestGitHook(t, nil)
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if _, err := g.InstallHook(context.Background(), repo, "pre-commit", "j1"); err != nil {
		t.Fatal(err)
	}
	if err := g.UninstallHook(context.Background(), repo, "pre-commit"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(hookPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected hook removed, stat err = %v", err)
	}
}

func TestUninstallRestoresPriorHook(t *testing.T) {
	g, _, repo := newTestGitHook(t, nil)
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	original := []byte("#!/bin/sh\necho original\n")
	if err := os.WriteFile(hookPath, original, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := g.InstallHook(context.Background(), repo, "pre-commit", "jx"); err != nil {
		t.Fatal(err)
	}
	// After install, content should be our wrapper.
	after, _ := os.ReadFile(hookPath)
	if !strings.Contains(string(after), "mcplexer run-job jx") {
		t.Errorf("install did not overwrite original: %s", after)
	}
	if err := g.UninstallHook(context.Background(), repo, "pre-commit"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	restored, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(restored) != string(original) {
		t.Errorf("restored content mismatch:\n got: %q\nwant: %q", restored, original)
	}
}

func TestInstallHookConfirmDenied(t *testing.T) {
	g, fs, repo := newTestGitHook(t, func(string) bool { return false })
	r, err := g.InstallHook(context.Background(), repo, "pre-commit", "denied")
	if !errors.Is(err, ErrNotConfirmed) {
		t.Errorf("err = %v, want ErrNotConfirmed", err)
	}
	if r != nil {
		t.Errorf("receipt = %+v, want nil", r)
	}
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("hook should not exist; stat err = %v", err)
	}
	if len(fs.receipts) != 0 {
		t.Errorf("expected 0 receipts on denial, got %d", len(fs.receipts))
	}
}

func TestInstallHookRequiresGitRepo(t *testing.T) {
	home := t.TempDir()
	notARepo := t.TempDir()
	g, _ := NewGitHookInstaller(home, &fakeReceiptStore{}, nil)
	_, err := g.InstallHook(context.Background(), notARepo, "pre-commit", "j")
	if err == nil || !strings.Contains(err.Error(), "not a git repo") {
		t.Errorf("expected not-a-git-repo error, got %v", err)
	}
}

func TestUninstallHookIdempotent(t *testing.T) {
	g, _, repo := newTestGitHook(t, nil)
	if err := g.UninstallHook(context.Background(), repo, "pre-commit"); err != nil {
		t.Errorf("uninstall on empty: %v", err)
	}
}

func TestNewGitHookInstallerValidation(t *testing.T) {
	if _, err := NewGitHookInstaller("", &fakeReceiptStore{}, nil); err == nil {
		t.Error("expected error on empty home")
	}
	if _, err := NewGitHookInstaller(t.TempDir(), nil, nil); err == nil {
		t.Error("expected error on nil store")
	}
}
