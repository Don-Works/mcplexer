package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

func gatewayTestBundle(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	return gatewayTestBundleWithModes(t, top, files, nil)
}

func gatewayTestBundleWithModes(
	t *testing.T, top string, files map[string]string, modes map[string]int64,
) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		mode := int64(0o644)
		if configured, ok := modes[name]; ok {
			mode = configured
		}
		hdr := &tar.Header{Name: top + "/" + name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func TestSkillInstallStagesReadOnlyBundleManifestBeforeActivation(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	body := "---\nname: read-only-install\ndescription: Neutral read-only install fixture.\n---\nREAD ONLY BODY\n"
	bundle := gatewayTestBundleWithModes(t, "read-only-install", map[string]string{
		"SKILL.md":  body,
		"asset.txt": "neutral asset\n",
	}, map[string]int64{"SKILL.md": 0o444})
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "read-only-install", Body: body, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish read-only bundle: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "installed")
	raw, _ := json.Marshal(map[string]any{"name": "read-only-install", "dest": dest})
	resp, rpcErr := h.handleSkillInstall(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("install rpc error: %v", rpcErr)
	}
	if text := toolResultText(t, resp); strings.Contains(text, "install failed") {
		t.Fatalf("read-only manifest blocked install: %s", text)
	}
	info, err := os.Stat(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("stat installed SKILL.md: %v", err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("installed rendered SKILL.md remained read-only: mode=%o", info.Mode().Perm())
	}
}

func TestSkillInstallWriteFailurePreservesExistingDestinationAndCleansStage(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "installed")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir existing dest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep.txt"), []byte("previous install\n"), 0o644); err != nil {
		t.Fatalf("write existing fixture: %v", err)
	}
	body := "---\nname: staged-failure\ndescription: Neutral staged failure fixture.\n---\nNEW BODY\n"
	bundle := gatewayTestBundle(t, "staged-failure", map[string]string{
		"SKILL.md": body,
		"new.txt":  "must not activate\n",
	})
	wantErr := errors.New("synthetic rendered write failure")
	_, err := installSkillBundleWithWriter(bundle, body, dest, true,
		func(string, []byte, os.FileMode) error { return wantErr })
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("install error = %v, want rendered write failure", err)
	}
	kept, err := os.ReadFile(filepath.Join(dest, "keep.txt"))
	if err != nil || string(kept) != "previous install\n" {
		t.Fatalf("previous destination changed: content=%q err=%v", kept, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("staged asset leaked into destination: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read install parent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(dest) {
		t.Fatalf("install stage was not cleaned: %+v", entries)
	}
}

func TestSkillInstallWritesRenderedBodyOverRawBundlePlaceholder(t *testing.T) {
	ctx := context.Background()
	h, reg := newSkillRegistryHandler(t)
	fragmentBody := "---\nname: install-fragment\ndescription: Neutral install fragment.\n---\nINSTALL FRAGMENT\n"
	fragment, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "install-fragment", Body: fragmentBody})
	if err != nil {
		t.Fatalf("publish fragment: %v", err)
	}
	rootBody := fmt.Sprintf(`---
name: install-composed
description: Neutral composed install fixture.
includes:
  - id: fragment
    skill: install-fragment
    scope: global
    version: %d
    content_hash: %q
---
INSTALL ROOT
<!-- mcpx:include fragment -->
`, fragment.Version, fragment.ContentHash)
	bundle := gatewayTestBundle(t, "install-composed", map[string]string{
		"SKILL.md":       rootBody,
		"scripts/run.sh": "echo install\n",
	})
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "install-composed", Body: rootBody, Bundle: bundle,
	}); err != nil {
		t.Fatalf("publish root: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "installed")
	raw, _ := json.Marshal(map[string]any{"name": "install-composed", "dest": dest})
	resp, rpcErr := h.handleSkillInstall(ctx, raw)
	if rpcErr != nil {
		t.Fatalf("install rpc error: %v", rpcErr)
	}
	if text := toolResultText(t, resp); !strings.Contains(text, "expanded_sha256") {
		t.Fatalf("install result lacks render integrity: %s", text)
	}
	installed, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	if !strings.Contains(string(installed), "INSTALL FRAGMENT") || strings.Contains(string(installed), "mcpx:include") {
		t.Fatalf("installed body contains raw placeholder: %s", installed)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts", "run.sh")); err != nil {
		t.Fatalf("bundle asset missing: %v", err)
	}
}
