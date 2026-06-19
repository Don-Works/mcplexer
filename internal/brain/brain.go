// Package brain implements the MCPlexer Brain: a git-backed,
// Markdown-canonical state repository. Each workspace's durable state
// (tasks, memories, workspace config) is materialised as a tree of
// Markdown files with YAML frontmatter, while the gateway continues to
// index that tree into SQLite for fast querying.
//
// M0 (this milestone) ships the foundation only: the feature flag, the
// per-entity frontmatter schema structs, a deterministic struct→bytes
// serializer, an adrg/frontmatter-backed parser, the inbound
// struct→DB-model converters, a validation layer, and the repo-scaffold
// writer. None of it is wired into the daemon yet — every code path is
// reachable ONLY when MCPLEXER_BRAIN_ENABLED is set, and flag-off is a
// complete no-op (today's behaviour, byte-for-byte).
//
// Source-of-truth model: the git-tracked file tree is canonical; the
// SQLite store is a derived, rebuildable index. See docs/brain.md §4.
package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env var names that drive the brain configuration.
const (
	// EnvEnabled toggles the whole feature. Truthy values: "1", "true"
	// (case-insensitive). Anything else (incl. unset) = disabled.
	EnvEnabled = "MCPLEXER_BRAIN_ENABLED"

	// EnvDir overrides the brain repo location. Defaults to a sibling
	// dir OUTSIDE ~/.mcplexer so the DB-lockdown hook stays untouched
	// (Appendix B decision #1).
	EnvDir = "MCPLEXER_BRAIN_DIR"
)

// DefaultDirName is the brain repo directory name under the user's home.
const DefaultDirName = "mcplexer-brain"

// Config holds the resolved brain configuration. The zero value is a
// disabled brain.
type Config struct {
	// Enabled gates every brain code path. When false the brain is a
	// complete no-op.
	Enabled bool

	// Dir is the absolute path to the brain repo root.
	Dir string
}

// LoadConfig resolves the brain configuration from the environment.
//
// getenv is injected (rather than calling os.Getenv directly) so tests
// can drive the resolution deterministically. Pass os.Getenv in
// production.
//
// Enabled is derived from MCPLEXER_BRAIN_ENABLED ("1"/"true", truthy).
// The daemon OR's this with settings.brain_enabled at wire time (M1+);
// M0 only reads the env. Dir is derived from MCPLEXER_BRAIN_DIR,
// defaulting to <home>/mcplexer-brain.
func LoadConfig(getenv func(string) string) Config {
	c := Config{
		Enabled: isTruthy(getenv(EnvEnabled)),
		Dir:     strings.TrimSpace(getenv(EnvDir)),
	}
	if c.Dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			// Fall back to a relative dir; the daemon validates the
			// path before use. Keeping this non-fatal mirrors the rest
			// of the brain's "log + degrade" posture.
			home = "."
		}
		c.Dir = filepath.Join(home, DefaultDirName)
	}
	return c
}

// WorkspaceDir returns the directory for a workspace slug:
// <Dir>/workspaces/<slug>. The slug is validated against path traversal
// before joining — a slug containing / or .. returns an error.
func (c Config) WorkspaceDir(slug string) (string, error) {
	s, err := safeSlug(slug)
	if err != nil {
		return "", fmt.Errorf("brain: WorkspaceDir(%q): %w", slug, err)
	}
	return filepath.Join(c.Dir, "workspaces", s), nil
}

// ClientDir returns the directory for a client/org slug:
// <Dir>/clients/<slug>. Defined now; consumed in M6.
func (c Config) ClientDir(slug string) (string, error) {
	s, err := safeSlug(slug)
	if err != nil {
		return "", fmt.Errorf("brain: ClientDir(%q): %w", slug, err)
	}
	return filepath.Join(c.Dir, "clients", s), nil
}

// GlobalDir returns the global (cross-workspace) directory: <Dir>/global.
func (c Config) GlobalDir() string {
	return filepath.Join(c.Dir, "global")
}

// SettingsKey is the JSON key in the settings singleton blob (migration
// 012) that mirrors the env flag. The daemon OR's the env flag with this
// at wire time (M1+); M0 only provides the parser + merge helper.
const SettingsKey = "brain_enabled"

// settingsShape is the minimal view of the settings JSON blob the brain
// cares about. Other settings keys are ignored.
type settingsShape struct {
	BrainEnabled *bool `json:"brain_enabled"`
}

// SettingsEnabled reports whether the settings JSON blob opts the brain
// in. A nil/absent key, malformed JSON, or empty blob all yield false
// (the safe default — flag-off is a complete no-op). raw is the
// json.RawMessage returned by store.SettingsStore.GetSettings.
func SettingsEnabled(raw json.RawMessage) bool {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return false
	}
	var s settingsShape
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return s.BrainEnabled != nil && *s.BrainEnabled
}

// MergeSettings returns a copy of c with Enabled OR'd with the
// settings-blob flag. The env flag (already in c.Enabled) and the
// settings flag are both honoured — either turns the brain on. This is
// the helper the daemon calls after loading settings (M1+ wiring); it
// keeps the env-first resolution in LoadConfig and layers settings on top
// without re-reading the environment.
func (c Config) MergeSettings(raw json.RawMessage) Config {
	c.Enabled = c.Enabled || SettingsEnabled(raw)
	return c
}

// isTruthy reports whether s is a recognised truthy flag value.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
