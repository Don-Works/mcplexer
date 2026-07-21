package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCommandsArchiveDefaultsOutsideCommandsDir(t *testing.T) {
	src := filepath.Join(t.TempDir(), ".claude", "commands")
	got := resolveCommandsArchive("", src)
	wantPrefix := src + ".migrated" + string(filepath.Separator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("archive path %q, want prefix %q", got, wantPrefix)
	}
	if strings.Contains(got, filepath.Join("commands", ".migrated")) {
		t.Fatalf("archive path must not sit under commands dir: %q", got)
	}
}

func TestResolveCommandsArchiveHonoursExplicitPath(t *testing.T) {
	got := resolveCommandsArchive("~/custom-command-archive", "/ignored")
	if strings.HasPrefix(got, "~") {
		t.Fatalf("explicit archive path was not home-expanded: %q", got)
	}
	if !strings.HasSuffix(got, "custom-command-archive") {
		t.Fatalf("explicit archive path %q, want custom-command-archive suffix", got)
	}
}
