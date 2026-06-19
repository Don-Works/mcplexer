package agentrules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.md")

	present, ver, up, err := Status(path, CurrentVersion)
	if err != nil {
		t.Fatalf("Status(missing): %v", err)
	}
	if present || up || ver != 0 {
		t.Errorf("missing file: got present=%v ver=%d up=%v; want false/0/false", present, ver, up)
	}
}

func TestStatusFileWithoutMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("just some markdown\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	present, ver, up, err := Status(path, CurrentVersion)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if present || up || ver != 0 {
		t.Errorf("no markers: got present=%v ver=%d up=%v", present, ver, up)
	}
}

func TestStatusInstalledAndCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	if _, err := Sync(path, CurrentVersion); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	present, ver, up, err := Status(path, CurrentVersion)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !present {
		t.Errorf("expected present=true")
	}
	if ver != CurrentVersion {
		t.Errorf("ver = %d, want %d", ver, CurrentVersion)
	}
	if !up {
		t.Errorf("expected upToDate=true on freshly-synced file")
	}
}

func TestStatusOlderVersionInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	body := "<!-- MCPLEXER:BEGIN v0 -->\n\nold body\n\n<!-- MCPLEXER:END -->\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	present, ver, up, err := Status(path, CurrentVersion)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !present {
		t.Errorf("expected present=true on installed v0")
	}
	if ver != 0 {
		t.Errorf("ver = %d, want 0", ver)
	}
	if up {
		t.Errorf("expected upToDate=false for older version")
	}
}
