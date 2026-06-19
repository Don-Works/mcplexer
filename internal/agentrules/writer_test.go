package agentrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncTable(t *testing.T) {
	type setup func(t *testing.T, path string)

	preamble := "# My Notes\n\nSome existing rules.\n"
	tail := "\n## After\n\nExisting trailing section.\n"

	// Helper: produce a "v0" block (older marker) for upgrade test.
	v0Block := "<!-- MCPLEXER:BEGIN v0 -->\n\nold content here\n\n<!-- MCPLEXER:END -->\n"

	tests := []struct {
		name              string
		setup             setup
		wantChanged       bool
		wantContains      []string
		wantNotContains   []string
		wantStartsWith    string
		wantEndsWithRegex string
	}{
		{
			name: "missing_file_is_created",
			setup: func(t *testing.T, path string) {
				// no file
			},
			wantChanged:  true,
			wantContains: []string{"<!-- MCPLEXER:BEGIN v1 -->", "<!-- MCPLEXER:END -->"},
		},
		{
			name: "no_markers_appended",
			setup: func(t *testing.T, path string) {
				writeFile(t, path, preamble)
			},
			wantChanged:    true,
			wantStartsWith: "# My Notes\n",
			wantContains:   []string{"<!-- MCPLEXER:BEGIN v1 -->", "prefer mcpx"},
		},
		{
			name: "v1_markers_idempotent",
			setup: func(t *testing.T, path string) {
				writeFile(t, path, preamble+Render(1)+tail)
			},
			wantChanged:  false,
			wantContains: []string{preamble, "<!-- MCPLEXER:BEGIN v1 -->", "After"},
		},
		{
			name: "v0_markers_upgrade",
			setup: func(t *testing.T, path string) {
				writeFile(t, path, preamble+v0Block+tail)
			},
			wantChanged: true,
			wantContains: []string{
				preamble,
				"<!-- MCPLEXER:BEGIN v1 -->",
				"prefer mcpx",
				"After", // trailing content preserved
			},
			wantNotContains: []string{
				"old content here",
				"BEGIN v0",
			},
		},
		{
			name: "mid_document_markers_replaced_in_place",
			setup: func(t *testing.T, path string) {
				body := "# Top\n\n" + v0Block + "\n\n## Middle\n\nmiddle text\n"
				writeFile(t, path, body)
			},
			wantChanged: true,
			wantContains: []string{
				"# Top",
				"<!-- MCPLEXER:BEGIN v1 -->",
				"## Middle",
				"middle text",
			},
			wantNotContains: []string{"old content here"},
		},
		{
			name: "preserves_content_outside_markers_byte_for_byte",
			setup: func(t *testing.T, path string) {
				body := "PREFIX LINE\n" + v0Block + "SUFFIX LINE\n"
				writeFile(t, path, body)
			},
			wantChanged: true,
			wantContains: []string{
				"PREFIX LINE",
				"SUFFIX LINE",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "CLAUDE.md")
			tc.setup(t, path)

			changed, err := Sync(path, 1)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}

			got := readFile(t, path)
			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in:\n%s", sub, got)
				}
			}
			for _, sub := range tc.wantNotContains {
				if strings.Contains(got, sub) {
					t.Errorf("unexpected %q in:\n%s", sub, got)
				}
			}
			if tc.wantStartsWith != "" && !strings.HasPrefix(got, tc.wantStartsWith) {
				t.Errorf("file does not start with %q; got prefix %q", tc.wantStartsWith, got[:min(len(got), len(tc.wantStartsWith))])
			}
		})
	}
}

func TestSyncTwiceNoOps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	if _, err := Sync(path, CurrentVersion); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	stat1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	first := readFile(t, path)

	changed, err := Sync(path, CurrentVersion)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if changed {
		t.Errorf("second Sync(version=%d) reported changed=true", CurrentVersion)
	}
	stat2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if stat1.Size() != stat2.Size() {
		t.Errorf("file size changed across no-op Sync: %d -> %d", stat1.Size(), stat2.Size())
	}
	second := readFile(t, path)
	if first != second {
		t.Errorf("file bytes changed across no-op Sync")
	}
}

func TestSyncCreatesParentDirWithRestrictedPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "CLAUDE.md")
	changed, err := Sync(path, CurrentVersion)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true for missing path")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created at %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file permissions = %04o, want 0600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("parent permissions = %04o, want 0700", got)
	}
}

func TestSyncExistingFilePermsUpdated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	writeFile(t, path, "# Existing\n")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("precondition: expected 0644, got %04o", info.Mode().Perm())
	}

	changed, err := Sync(path, CurrentVersion)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file permissions = %04o, want 0600", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestDryRun(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, path string)
		wantChange  bool
		wantMarkers bool
	}{
		{
			name:       "missing_file_would_create",
			setup:      func(t *testing.T, path string) {},
			wantChange: true,
		},
		{
			name: "no_markers_would_append",
			setup: func(t *testing.T, path string) {
				writeFile(t, path, "# My Notes\n")
			},
			wantChange: true,
		},
		{
			name: "current_version_no_change",
			setup: func(t *testing.T, path string) {
				writeFile(t, path, "# Top\n\n"+Render(CurrentVersion)+"\n## Bottom\n")
			},
			wantChange:  false,
			wantMarkers: true,
		},
		{
			name: "old_version_would_change",
			setup: func(t *testing.T, path string) {
				writeFile(t, path, "# Top\n\n<!-- MCPLEXER:BEGIN v0 -->\n\nold\n\n<!-- MCPLEXER:END -->\n\n## Bottom\n")
			},
			wantChange:  true,
			wantMarkers: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "CLAUDE.md")
			tc.setup(t, path)

			dr, err := DryRun(path, CurrentVersion)
			if err != nil {
				t.Fatalf("DryRun: %v", err)
			}
			if dr.WouldChange != tc.wantChange {
				t.Errorf("WouldChange = %v, want %v", dr.WouldChange, tc.wantChange)
			}
			if dr.MarkersFound != tc.wantMarkers {
				t.Errorf("MarkersFound = %v, want %v", dr.MarkersFound, tc.wantMarkers)
			}
			if tc.wantChange && dr.NewContent == "" {
				t.Error("WouldChange but NewContent is empty")
			}
			if !tc.wantChange && dr.NewContent != "" {
				t.Error("no change but NewContent is non-empty")
			}
		})
	}
}

func TestDryRunFileUnmodified(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	original := "# Notes\n\n" + Render(CurrentVersion) + "\n"
	writeFile(t, path, original)

	_, err := DryRun(path, CurrentVersion)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	got := readFile(t, path)
	if got != original {
		t.Error("DryRun modified the file")
	}
}
