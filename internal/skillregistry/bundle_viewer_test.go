package skillregistry_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestBundleFileIndexBasic(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("idx-test", "Use when testing file index.")
	bundle := buildBundle(t, "idx-test", map[string]string{
		"SKILL.md":           body,
		"scripts/run.mjs":    "console.log('hello');\n",
		"reference/guide.md": "# Guide\nSome text.\n",
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "idx-test", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	entries, err := reg.BundleFileIndex(ctx, skillregistry.AdminScope(), "idx-test", skillregistry.VersionRef{Latest: true})
	if err != nil {
		t.Fatalf("BundleFileIndex: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	byPath := map[string]*skillregistry.BundleFileEntry{}
	for i := range entries {
		byPath[entries[i].Path] = &entries[i]
	}

	if e, ok := byPath["SKILL.md"]; !ok {
		t.Fatal("SKILL.md not in index")
	} else {
		if !e.IsText {
			t.Error("SKILL.md should be text")
		}
		if e.Role != skillregistry.RoleManifest {
			t.Errorf("SKILL.md role = %q, want manifest", e.Role)
		}
		if e.SHA256 == "" {
			t.Error("SKILL.md missing sha256")
		}
	}

	if e, ok := byPath["scripts/run.mjs"]; !ok {
		t.Fatal("scripts/run.mjs not in index")
	} else {
		if e.Role != skillregistry.RoleScript {
			t.Errorf("scripts/run.mjs role = %q, want script", e.Role)
		}
	}

	if e, ok := byPath["reference/guide.md"]; !ok {
		t.Fatal("reference/guide.md not in index")
	} else {
		if e.Role != skillregistry.RoleRef {
			t.Errorf("reference/guide.md role = %q, want reference", e.Role)
		}
	}
}

func TestBundleFileIndexNoBundle(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("no-bundle-idx", "Use when testing no bundle.")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "no-bundle-idx", Body: body}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, err := reg.BundleFileIndex(ctx, skillregistry.AdminScope(), "no-bundle-idx", skillregistry.VersionRef{Latest: true})
	if !errors.Is(err, skillregistry.ErrBundleNotPresent) {
		t.Fatalf("expected ErrBundleNotPresent, got %v", err)
	}
}

func TestBundleFileIndexMissingSkill(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	_, err := reg.BundleFileIndex(ctx, skillregistry.AdminScope(), "nonexistent", skillregistry.VersionRef{Latest: true})
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
}

func TestBundleFileContentText(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("fc-text", "Use when testing file content read.")
	scriptContent := "#!/usr/bin/env node\nconsole.log('hello');\n"
	bundle := buildBundle(t, "fc-text", map[string]string{
		"SKILL.md":        body,
		"scripts/run.mjs": scriptContent,
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "fc-text", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	fc, err := reg.BundleFileContent(ctx, skillregistry.AdminScope(), "fc-text", skillregistry.VersionRef{Latest: true}, "scripts/run.mjs")
	if err != nil {
		t.Fatalf("BundleFileContent: %v", err)
	}
	if !fc.IsText {
		t.Error("expected IsText=true")
	}
	if fc.Content != scriptContent {
		t.Errorf("content mismatch: got %q, want %q", fc.Content, scriptContent)
	}
	if fc.Truncated {
		t.Error("should not be truncated")
	}
}

func TestBundleFileContentSKILLMD(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("fc-skill", "Use when reading SKILL.md from bundle.")
	bundle := buildBundle(t, "fc-skill", map[string]string{
		"SKILL.md": body,
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "fc-skill", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	fc, err := reg.BundleFileContent(ctx, skillregistry.AdminScope(), "fc-skill", skillregistry.VersionRef{Latest: true}, "SKILL.md")
	if err != nil {
		t.Fatalf("BundleFileContent: %v", err)
	}
	if !fc.IsText {
		t.Error("SKILL.md should be text")
	}
	if fc.Content != body {
		t.Errorf("SKILL.md content mismatch:\ngot:\n%s\nwant:\n%s", fc.Content, body)
	}
}

func TestBundleFileContentTraversal(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("traversal", "Use when testing path traversal.")
	bundle := buildBundle(t, "traversal", map[string]string{"SKILL.md": body})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "traversal", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cases := []string{"../etc/passwd", "/etc/passwd", "../../secret"}
	for _, p := range cases {
		_, err := reg.BundleFileContent(ctx, skillregistry.AdminScope(), "traversal", skillregistry.VersionRef{Latest: true}, p)
		if err == nil {
			t.Errorf("expected error for path %q, got nil", p)
		}
	}
}

func TestBundleFileContentNotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("fc-notfound", "Use when testing missing file in bundle.")
	bundle := buildBundle(t, "fc-notfound", map[string]string{"SKILL.md": body})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "fc-notfound", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, err := reg.BundleFileContent(ctx, skillregistry.AdminScope(), "fc-notfound", skillregistry.VersionRef{Latest: true}, "nonexistent.txt")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestBundleFileContentBinary(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("fc-binary", "Use when testing binary file.")
	binaryData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}
	bundle := buildBundleWithBinary(t, "fc-binary", map[string]string{
		"SKILL.md": body,
	}, map[string][]byte{
		"image.png": binaryData,
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "fc-binary", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	fc, err := reg.BundleFileContent(ctx, skillregistry.AdminScope(), "fc-binary", skillregistry.VersionRef{Latest: true}, "image.png")
	if err != nil {
		t.Fatalf("BundleFileContent: %v", err)
	}
	if fc.IsText {
		t.Error("binary file should not be text")
	}
	if fc.ContentB64 == "" {
		t.Error("expected base64 content for binary file")
	}
	if fc.Content != "" {
		t.Error("expected empty Content for binary file")
	}
}

func TestBundleFileContentTruncation(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("fc-trunc", "Use when testing large file truncation.")
	bigContent := strings.Repeat("x", 600*1024)
	bundle := buildBundle(t, "fc-trunc", map[string]string{
		"SKILL.md": body,
		"big.txt":  bigContent,
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "fc-trunc", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	fc, err := reg.BundleFileContent(ctx, skillregistry.AdminScope(), "fc-trunc", skillregistry.VersionRef{Latest: true}, "big.txt")
	if err != nil {
		t.Fatalf("BundleFileContent: %v", err)
	}
	if !fc.Truncated {
		t.Error("expected truncated=true for file > 512KB")
	}
	if len(fc.Content) > 512*1024 {
		t.Errorf("content too large: %d bytes", len(fc.Content))
	}
}

func TestDiffVersionsBodyChange(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body1 := sampleBody("diff-test", "Version one.")
	body2 := sampleBody("diff-test", "Version two.")

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-test", Body: body1}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-test", Body: body2}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	diff, err := reg.DiffVersions(ctx, skillregistry.AdminScope(), "diff-test",
		skillregistry.VersionRef{Version: 1},
		skillregistry.VersionRef{Version: 2},
	)
	if err != nil {
		t.Fatalf("DiffVersions: %v", err)
	}
	if diff.OldVersion != 1 || diff.NewVersion != 2 {
		t.Fatalf("versions mismatch: old=%d new=%d", diff.OldVersion, diff.NewVersion)
	}
	if diff.BodyDiff == "" {
		t.Error("expected non-empty body diff")
	}
	if !strings.Contains(diff.BodyDiff, "Version one") {
		t.Errorf("body diff should contain old text: %s", diff.BodyDiff)
	}
	if !strings.Contains(diff.BodyDiff, "Version two") {
		t.Errorf("body diff should contain new text: %s", diff.BodyDiff)
	}
}

func TestDiffVersionsIdentical(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("diff-same", "Same content.")

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-same", Body: body}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-same", Body: body}); err != nil {
		t.Fatalf("publish dedup: %v", err)
	}

	diff, err := reg.DiffVersions(ctx, skillregistry.AdminScope(), "diff-same",
		skillregistry.VersionRef{Version: 1},
		skillregistry.VersionRef{Latest: true},
	)
	if err != nil {
		t.Fatalf("DiffVersions: %v", err)
	}
	if diff.BodyDiff != "" {
		t.Errorf("expected empty body diff for identical versions, got %q", diff.BodyDiff)
	}
}

func TestDiffVersionsBundleTree(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body1 := sampleBody("diff-bundle", "Version one.")
	bundle1 := buildBundle(t, "diff-bundle", map[string]string{
		"SKILL.md":       body1,
		"scripts/run.sh": "echo v1\n",
		"readme.md":      "# Readme v1\n",
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "diff-bundle", Body: body1, Bundle: bundle1,
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	body2 := sampleBody("diff-bundle", "Version two.")
	bundle2 := buildBundle(t, "diff-bundle", map[string]string{
		"SKILL.md":       body2,
		"scripts/run.sh": "echo v2\n",
		"new-file.txt":   "added\n",
	})

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "diff-bundle", Body: body2, Bundle: bundle2,
	}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	diff, err := reg.DiffVersions(ctx, skillregistry.AdminScope(), "diff-bundle",
		skillregistry.VersionRef{Version: 1},
		skillregistry.VersionRef{Version: 2},
	)
	if err != nil {
		t.Fatalf("DiffVersions: %v", err)
	}

	if !diff.OldHasBundle || !diff.NewHasBundle {
		t.Fatal("both versions should have bundles")
	}

	treeMap := map[string]string{}
	for _, e := range diff.Tree {
		treeMap[e.Path] = e.Status
	}

	if s, ok := treeMap["readme.md"]; !ok || s != "removed" {
		t.Errorf("readme.md should be removed, got %q", s)
	}
	if s, ok := treeMap["scripts/run.sh"]; !ok || s != "modified" {
		t.Errorf("scripts/run.sh should be modified, got %q", s)
	}
	if s, ok := treeMap["new-file.txt"]; !ok || s != "added" {
		t.Errorf("new-file.txt should be added, got %q", s)
	}
}

func TestDiffVersionsMissingOld(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body := sampleBody("diff-missing", "Only version.")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-missing", Body: body}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, err := reg.DiffVersions(ctx, skillregistry.AdminScope(), "diff-missing",
		skillregistry.VersionRef{Version: 99},
		skillregistry.VersionRef{Version: 1},
	)
	if err == nil {
		t.Fatal("expected error for missing old version")
	}
}

func TestDiffVersionsDeletedVersion(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body1 := sampleBody("diff-deleted", "Version one.")
	body2 := sampleBody("diff-deleted", "Version two.")

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-deleted", Body: body1}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "diff-deleted", Body: body2}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	if err := reg.SoftDelete(ctx, nil, "diff-deleted", 1); err != nil {
		t.Fatalf("delete v1: %v", err)
	}

	_, err := reg.DiffVersions(ctx, skillregistry.AdminScope(), "diff-deleted",
		skillregistry.VersionRef{Version: 1},
		skillregistry.VersionRef{Version: 2},
	)
	if err == nil {
		t.Fatal("expected error for deleted old version")
	}
}

func TestDiffVersionsFrontmatterChange(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	body1 := "---\nname: fm-diff\ndescription: V1\ntags: [alpha]\n---\n# Body\nSame body."
	body2 := "---\nname: fm-diff\ndescription: V2\ntags: [beta, gamma]\n---\n# Body\nSame body."

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "fm-diff", Body: body1}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "fm-diff", Body: body2}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	diff, err := reg.DiffVersions(ctx, skillregistry.AdminScope(), "fm-diff",
		skillregistry.VersionRef{Version: 1},
		skillregistry.VersionRef{Version: 2},
	)
	if err != nil {
		t.Fatalf("DiffVersions: %v", err)
	}
	if diff.BodyDiff == "" {
		t.Error("expected non-empty body diff (body includes frontmatter)")
	}
	if diff.FrontDiff == "" {
		t.Error("expected non-empty frontmatter diff")
	}
	if !strings.Contains(diff.FrontDiff, "V1") || !strings.Contains(diff.FrontDiff, "V2") {
		t.Errorf("frontmatter diff should contain V1 and V2: %s", diff.FrontDiff)
	}
}

// buildBundleWithBinary creates a bundle with both text and binary files.
func buildBundleWithBinary(t *testing.T, topDir string, textFiles map[string]string, binaryFiles map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range textFiles {
		full := name
		if topDir != "" {
			full = topDir + "/" + name
		}
		if err := tw.WriteHeader(&tar.Header{Name: full, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	for name, content := range binaryFiles {
		full := name
		if topDir != "" {
			full = topDir + "/" + name
		}
		if err := tw.WriteHeader(&tar.Header{Name: full, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}
