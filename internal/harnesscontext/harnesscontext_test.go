package harnesscontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarvestCodexAndCursorContext(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), "# User Codex\n\nUse mcplexer memory.")
	mustWrite(t, filepath.Join(work, "AGENTS.md"), "# Repo Agents\n\nFollow CLAUDE.md.")
	mustWrite(t, filepath.Join(work, ".cursor", "rules", "go.mdc"), "# Go\n\nUse table tests.")

	res, err := Harvest(Options{
		Harnesses:      []Harness{HarnessAll},
		HomeDir:        home,
		WorkDir:        work,
		HarvestBatchID: "harvest-test",
	})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if res.Ingested != 3 {
		t.Fatalf("ingested = %d, want 3; files=%+v", res.Ingested, res.Files)
	}
	seen := map[string]bool{}
	for _, doc := range res.Documents {
		seen[doc.SourceKind] = true
		if doc.HarvestBatchID != "harvest-test" {
			t.Fatalf("batch = %q, want harvest-test", doc.HarvestBatchID)
		}
		if doc.SourceHash == "" || !strings.HasPrefix(doc.SourceHash, "hctx-") {
			t.Fatalf("missing source hash: %+v", doc)
		}
	}
	for _, kind := range []string{"codex_user", "codex_workspace", "cursor_workspace_rule"} {
		if !seen[kind] {
			t.Fatalf("missing source kind %s in %+v", kind, seen)
		}
	}
}

func TestHarvestExcludesSecretLikeFiles(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret")
	mustWrite(t, filepath.Join(work, ".cursor", "rules", "api-key.mdc"), "# Secret path\n")

	res, err := Harvest(Options{
		Harnesses: []Harness{HarnessAll},
		HomeDir:   home,
		WorkDir:   work,
	})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if res.Ingested != 0 {
		t.Fatalf("ingested = %d, want 0", res.Ingested)
	}
	if res.Excluded != 2 {
		t.Fatalf("excluded = %d, want 2; files=%+v", res.Excluded, res.Files)
	}
}

func TestHarvestHonorsLimits(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	mustWrite(t, filepath.Join(home, ".codex", "AGENTS.md"), "# First\n\nsmall")
	mustWrite(t, filepath.Join(work, "AGENTS.md"), "# Second\n\nsmall")

	res, err := Harvest(Options{
		Harnesses:      []Harness{HarnessCodex},
		HomeDir:        home,
		WorkDir:        work,
		MaxFiles:       1,
		MaxFileBytes:   1024,
		MaxTotalBytes:  1024,
		HarvestBatchID: "harvest-limit",
	})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if res.Ingested != 1 || res.Skipped != 1 {
		t.Fatalf("ingested/skipped = %d/%d, want 1/1; files=%+v", res.Ingested, res.Skipped, res.Files)
	}
}

func TestBuildDataItems(t *testing.T) {
	items, err := BuildDataItems([]Document{{
		Harness:        "codex",
		SourcePath:     "/tmp/AGENTS.md",
		SourceKind:     "codex_workspace",
		Title:          "Agents",
		Content:        "Use tests.",
		SourceHash:     "hctx-test",
		HarvestBatchID: "harvest-test",
	}})
	if err != nil {
		t.Fatalf("BuildDataItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].Text != "Use tests." {
		t.Fatalf("item text = %q", items[0].Text)
	}
	if !strings.Contains(string(items[0].PayloadJSON), `"source_kind":"codex_workspace"`) {
		t.Fatalf("payload missing source kind: %s", items[0].PayloadJSON)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
