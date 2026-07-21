// Package harnesssync generalizes the rules-sync / agentrules mechanism
// to per-harness bootstrap of the canonical "using-mcplexer" skill (v1
// from the registry, materialized as seeds/using-mcplexer.md).
//
// It renders ONE skill body to harness-specific artifacts:
//   - claude: ~/.claude/skills/using-mcplexer/SKILL.md (verbatim) + slim
//     managed pointer block in ~/.claude/CLAUDE.md
//   - opencode: ~/.config/opencode/skills/using-mcplexer/SKILL.md (verbatim) +
//     slim managed pointer block in ~/.config/opencode/AGENTS.md
//   - codex:   ~/.codex/AGENTS.md with managed block
//   - gemini:  ~/.gemini/GEMINI.md with managed block
//   - grok:    ~/.grok/config.toml instructions (comment block)
//   - mimo:    ~/.config/mimocode/AGENTS.md with managed block
//   - pi:      ~/.pi/AGENTS.md with managed block
//
// Content-hash receipts (bootstrap_hash) + drifted flag are stored for
// recheck. Reusable Status/Install/Recheck. Used by CLI "harness sync"
// and /api/v1/setup/* endpoints.
//
// Markers are versioned and harness-scoped so they coexist with the
// older MCPLEXER:BEGIN rules block and do not fight.
package harnesssync

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// HarnessKey is the stable key used in CLI, REST, store, and status.
type HarnessKey string

const (
	Claude   HarnessKey = "claude"
	Codex    HarnessKey = "codex"
	OpenCode HarnessKey = "opencode"
	Gemini   HarnessKey = "gemini"
	Grok     HarnessKey = "grok"
	MiMo     HarnessKey = "mimo"
	Pi       HarnessKey = "pi"
)

// AllKeys returns the supported harness keys in display order.
func AllKeys() []HarnessKey {
	return []HarnessKey{Claude, Codex, OpenCode, Gemini, Grok, MiMo, Pi}
}

// Valid reports whether k is a recognized harness key.
func Valid(k HarnessKey) bool {
	switch k {
	case Claude, Codex, OpenCode, Gemini, Grok, MiMo, Pi:
		return true
	}
	return false
}

// ClientIDForMCP returns the install.ClientID used to determine mcp_wired
// and config_path for this harness (via install.Manager).
func ClientIDForMCP(k HarnessKey) string {
	switch k {
	case Claude:
		return "claude_code"
	case Codex:
		return "codex"
	case OpenCode:
		return "opencode"
	case Gemini:
		return "gemini_cli"
	case Grok:
		return "grok"
	case MiMo:
		return "mimocode"
	case Pi:
		return ""
	default:
		return ""
	}
}

// TargetPath returns the primary file that receives the managed block
// for the harness (relative to home). For claude this is the CLAUDE.md
// pointer location; the SKILL.md is a side artifact.
func TargetPath(home string, k HarnessKey) string {
	if home == "" {
		home = "."
	}
	switch k {
	case Claude:
		return filepath.Join(home, ".claude", "CLAUDE.md")
	case Codex:
		return filepath.Join(home, ".codex", "AGENTS.md")
	case OpenCode:
		return filepath.Join(home, ".config", "opencode", "AGENTS.md")
	case Gemini:
		return filepath.Join(home, ".gemini", "GEMINI.md")
	case Grok:
		return filepath.Join(home, ".grok", "config.toml")
	case MiMo:
		return filepath.Join(home, ".config", "mimocode", "AGENTS.md")
	case Pi:
		return filepath.Join(home, ".pi", "AGENTS.md")
	default:
		return ""
	}
}

// ClaudeSkillPath is the location for the verbatim using-mcplexer SKILL.md
// (claude only).
func ClaudeSkillPath(home string) string {
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".claude", "skills", "using-mcplexer", "SKILL.md")
}

// OpenCodeSkillPath is the location for the verbatim using-mcplexer SKILL.md
// sidecar for OpenCode (written to the native skills directory).
func OpenCodeSkillPath(home string) string {
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "opencode", "skills", "using-mcplexer", "SKILL.md")
}

// HarnessStatus is the row shape for one harness (matches REST contract).
type HarnessStatus struct {
	Key                string     `json:"key"`
	MCPWired           bool       `json:"mcp_wired"`
	ConfigPath         string     `json:"config_path"`
	LastInitializeAt   *time.Time `json:"last_initialize_at"`
	ClientInfo         *string    `json:"client_info"`
	BootstrapInstalled bool       `json:"bootstrap_installed"`
	BootstrapVersion   *int       `json:"bootstrap_version"`
	RegistryVersion    int        `json:"registry_version"`
	Drifted            bool       `json:"drifted"`
	// Accretion: local harness skills/commands that bypass the registry and
	// bloat sessions through native harness discovery. Nil when clean.
	Accretion *AccretionReport `json:"accretion,omitempty"`
}

// Block markers (harness-scoped, coexist with agentrules MCPLEXER:BEGIN).
// Most harness instruction files are markdown and can use HTML comments.
// Grok stores its bootstrap in config.toml, so it uses TOML line comments.
const (
	BlockBeginFmt = "<!-- MCPLEXER:HARNESS-SYNC:BEGIN v%d (%s) -->\n\n"
	BlockEnd      = "<!-- MCPLEXER:HARNESS-SYNC:END -->\n"

	TOMLBlockBeginFmt = "# MCPLEXER:HARNESS-SYNC:BEGIN v%d (%s)\n#\n"
	TOMLBlockEnd      = "# MCPLEXER:HARNESS-SYNC:END\n"
)

// normalizeHashBody matches the agentrules normalize for consistent
// content-hash receipts across the two systems.
func normalizeHashBody(b string) string {
	return strings.TrimSpace(b)
}

// contentHash returns sha256 hex of the normalized body.
func contentHash(b string) string {
	sum := sha256.Sum256([]byte(normalizeHashBody(b)))
	return fmt.Sprintf("%x", sum)
}
