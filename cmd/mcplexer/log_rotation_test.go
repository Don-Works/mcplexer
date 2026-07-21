package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureLogFile_CreatesAt0600 verifies that a fresh log file is created
// with owner-only perms. Pre-fix this path used 0644 — the regression we're
// guarding against here is "log file world-readable on multi-user box".
func TestEnsureLogFile_CreatesAt0600(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mcplexer.log")

	if err := ensureLogFile(logPath); err != nil {
		t.Fatalf("ensureLogFile: %v", err)
	}

	st, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0600 {
		t.Errorf("log file mode = %o, want 0600", got)
	}
}

// TestEnsureLogFile_TightensLegacyMode verifies that an existing 0644 file
// (a legacy artefact from a pre-fix daemon run) is chmod'd down to 0600.
// Without this, simply upgrading the binary would not heal the leak.
func TestEnsureLogFile_TightensLegacyMode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mcplexer.log")

	// Simulate a legacy file written by the old 0644 daemon path.
	if err := os.WriteFile(logPath, []byte("legacy line\n"), 0644); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	if err := ensureLogFile(logPath); err != nil {
		t.Fatalf("ensureLogFile: %v", err)
	}

	st, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0600 {
		t.Errorf("after ensure, mode = %o, want 0600", got)
	}
	// Content preserved (append semantics).
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "legacy line\n" {
		t.Errorf("legacy content corrupted: %q", data)
	}
}

// TestEnsureLogFile_Idempotent confirms repeated calls are safe — required
// because daemon start, launchd install, and serve.go init can all chain
// through this helper on a single boot.
func TestEnsureLogFile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mcplexer.log")

	for i := 0; i < 3; i++ {
		if err := ensureLogFile(logPath); err != nil {
			t.Fatalf("ensureLogFile (iter %d): %v", i, err)
		}
	}
	st, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0600 {
		t.Errorf("after %d calls, mode = %o, want 0600", 3, got)
	}
}

// TestOpenRotatingLog_RotatesOnSizeOverflow writes enough bytes to exceed
// MaxSize and asserts that lumberjack created a backup file alongside the
// fresh active log. Catches a wiring regression where the writer is
// constructed but not actually rotation-aware (e.g. someone reverted to
// os.OpenFile in a refactor).
func TestOpenRotatingLog_RotatesOnSizeOverflow(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "mcplexer.log")

	cfg := LogRotationConfig{
		MaxSizeMB:  1, // 1MB threshold so the test stays fast
		MaxBackups: 3,
		MaxAgeDays: 0,
		Compress:   false, // no gzip — keeps assertion simple
	}

	w, err := openRotatingLog(logPath, cfg)
	if err != nil {
		t.Fatalf("openRotatingLog: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Write ~1.5MB to force at least one rotation. 1KB chunks ×1500.
	chunk := bytes.Repeat([]byte("x"), 1024)
	for i := 0; i < 1500; i++ {
		line := append([]byte(fmt.Sprintf("%04d ", i)), chunk...)
		line = append(line, '\n')
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write line %d: %v", i, err)
		}
	}

	// Flush + close to let lumberjack finalise the rotation.
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	// Active file must still exist with the canonical name.
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("active log gone after rotation: %v", err)
	}

	// At least one backup must exist. Lumberjack names backups
	// `<base>-<timestamp>.log`, e.g. `mcplexer-2026-05-21T10-15-30.000.log`.
	backups := 0
	for _, e := range entries {
		name := e.Name()
		if name == "mcplexer.log" {
			continue
		}
		if strings.HasPrefix(name, "mcplexer-") && strings.HasSuffix(name, ".log") {
			backups++
		}
	}
	if backups < 1 {
		t.Errorf("expected >=1 lumberjack backup file, found 0 (dir=%v)", entries)
	}
}

// TestLoadLogRotationConfig_DefaultsWhenAbsent confirms that a missing or
// empty mcplexer.yaml falls back to the hardcoded defaults — important
// because most installs never customise rotation.
func TestLoadLogRotationConfig_DefaultsWhenAbsent(t *testing.T) {
	got := loadLogRotationConfig("")
	want := DefaultLogRotationConfig()
	if got != want {
		t.Errorf("empty path: got %+v, want %+v", got, want)
	}

	got = loadLogRotationConfig("/nonexistent/path/mcplexer.yaml")
	if got != want {
		t.Errorf("nonexistent path: got %+v, want %+v", got, want)
	}
}

// TestLoadLogRotationConfig_OverridesFromYAML confirms a real yaml block
// is honoured. Zero-valued fields stay at defaults (so users can override
// just one knob without restating all four).
func TestLoadLogRotationConfig_OverridesFromYAML(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "mcplexer.yaml")
	yaml := `log_rotation:
  max_size_mb: 10
  max_backups: 2
  compress: false
`
	if err := os.WriteFile(yamlPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	got := loadLogRotationConfig(yamlPath)
	if got.MaxSizeMB != 10 {
		t.Errorf("MaxSizeMB = %d, want 10", got.MaxSizeMB)
	}
	if got.MaxBackups != 2 {
		t.Errorf("MaxBackups = %d, want 2", got.MaxBackups)
	}
	// MaxAgeDays wasn't in the YAML — should keep default.
	if got.MaxAgeDays != DefaultLogRotationConfig().MaxAgeDays {
		t.Errorf("MaxAgeDays = %d, want default %d",
			got.MaxAgeDays, DefaultLogRotationConfig().MaxAgeDays)
	}
	if got.Compress {
		t.Errorf("Compress = true, want false")
	}
}
