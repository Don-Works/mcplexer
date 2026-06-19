package skillregistry_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// makeTmpGitRepo creates a self-contained git repo with one SKILL.md
// committed at HEAD. Returns the file:// URL the GitSource will clone
// from. Skips the test cleanly if git is unavailable on the runner.
func makeTmpGitRepo(t *testing.T, skillBody string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed on this runner")
	}
	repo := t.TempDir()
	skillDir := filepath.Join(repo, "impeccable-test")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	// Also drop a fake reference asset so we can confirm it survives the clone.
	if err := os.MkdirAll(filepath.Join(skillDir, "reference"), 0o755); err != nil {
		t.Fatalf("mkdir reference: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "reference", "notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=test@example.com", "-c", "user.name=Test", "config", "commit.gpgsign", "false"},
		{"add", "."},
		{
			"-c", "user.email=test@example.com",
			"-c", "user.name=Test",
			"-c", "commit.gpgsign=false",
			"commit", "-q", "-m", "init",
		},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return "file://" + repo
}

const validSkillBody = `---
name: impeccable-test
description: Use when running the GitSource integration test — fixture skill committed inside a tmp repo.
---
# Impeccable test

Body content for the integration fixture.
`

func TestGitSourceCloneIntegration(t *testing.T) {
	url := makeTmpGitRepo(t, validSkillBody)
	dataDir := t.TempDir()
	src := skillregistry.NewGitSource(dataDir)

	got, err := src.Clone(context.Background(), url, "")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if got.LocalPath == "" {
		t.Fatal("LocalPath empty")
	}
	if !strings.HasPrefix(got.LocalPath, dataDir) {
		t.Errorf("LocalPath %q escapes dataDir %q", got.LocalPath, dataDir)
	}
	if len(got.Commit) != 40 {
		t.Errorf("commit length = %d (want 40-char sha)", len(got.Commit))
	}

	// SKILL.md must be present + readable from the cloned tree.
	mdPath := filepath.Join(got.LocalPath, "impeccable-test", "SKILL.md")
	body, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read cloned SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "impeccable-test") {
		t.Errorf("SKILL.md content unexpected: %q", string(body))
	}

	// Reference asset survives — that's the entire point of the
	// "git" / "path" source type vs inline.
	if _, err := os.Stat(filepath.Join(got.LocalPath, "impeccable-test", "reference", "notes.md")); err != nil {
		t.Errorf("reference asset missing: %v", err)
	}

	// Re-cloning the same URL+ref must reuse the directory and not
	// error (fast-forward branch). Different invocations produce the
	// same key.
	got2, err := src.Clone(context.Background(), url, "")
	if err != nil {
		t.Fatalf("re-clone: %v", err)
	}
	if got2.LocalPath != got.LocalPath {
		t.Errorf("re-clone path drift: %q vs %q", got2.LocalPath, got.LocalPath)
	}
	if got2.Commit != got.Commit {
		t.Errorf("commit drift between identical clones: %q vs %q", got2.Commit, got.Commit)
	}
}

func TestGitSourceCloneRejectsEmptyURL(t *testing.T) {
	src := skillregistry.NewGitSource(t.TempDir())
	_, err := src.Clone(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

// TestPublishViaGitSourceMetadata exercises the end-to-end shape an
// importer relies on: clone → publish with MetadataExtras + path source.
// The resulting registry row must carry source_type=git, source_path,
// and the (url, ref, commit) trio inside metadata.source.
func TestPublishViaGitSourceMetadata(t *testing.T) {
	url := makeTmpGitRepo(t, validSkillBody)
	dataDir := t.TempDir()
	src := skillregistry.NewGitSource(dataDir)
	clone, err := src.Clone(context.Background(), url, "")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	reg, _ := newTestRegistry(t)
	body, err := os.ReadFile(filepath.Join(clone.LocalPath, "impeccable-test", "SKILL.md"))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	res, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Name:               "impeccable-test",
		Body:               string(body),
		Author:             "test",
		SourcePath:         filepath.Join(clone.LocalPath, "impeccable-test"),
		SourceTypeOverride: "git",
		MetadataExtras: map[string]any{
			"source": map[string]any{
				"type":       "git",
				"git_url":    clone.URL,
				"git_commit": clone.Commit,
			},
		},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Action != "created" {
		t.Errorf("action = %q (want created)", res.Action)
	}

	got, err := reg.Get(context.Background(), skillregistry.AdminScope(), "impeccable-test", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SourceType != "git" {
		t.Errorf("source_type = %q (want git)", got.SourceType)
	}
	if got.SourcePath == "" {
		t.Error("source_path empty")
	}
	if !strings.Contains(string(got.MetadataJSON), clone.Commit) {
		t.Errorf("metadata missing commit: %s", got.MetadataJSON)
	}
	if !strings.Contains(string(got.MetadataJSON), "git_url") {
		t.Errorf("metadata missing git_url: %s", got.MetadataJSON)
	}
}
