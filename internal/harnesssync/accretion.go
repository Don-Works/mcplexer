package harnesssync

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AccretionReport lists local harness artifacts that bypass the registry —
// skill directories other than using-mcplexer and flat command files.
// Each one can re-add its description to sessions through the harness's
// native discovery path, which is exactly the bloat the registry exists
// to absorb.
type AccretionReport struct {
	// ExtraSkills are discovered skill directories containing a SKILL.md,
	// excluding the harness-sync-owned using-mcplexer and hidden/system dirs.
	ExtraSkills []string `json:"extra_skills,omitempty"`
	// ExtraCommands are discovered flat *.md command files.
	ExtraCommands []string `json:"extra_commands,omitempty"`
}

// Empty reports whether nothing has accreted.
func (a AccretionReport) Empty() bool {
	return len(a.ExtraSkills) == 0 && len(a.ExtraCommands) == 0
}

// DetectAccretion scans Claude's local skill + command directories under
// home. Kept for older call sites; new code should use DetectHarnessAccretion.
func DetectAccretion(home string) AccretionReport {
	return DetectHarnessAccretion(home, Claude)
}

// DetectHarnessAccretion scans harness-native global skill/command paths.
// Missing directories read as empty, never as an error: a clean machine is
// the goal state. Project-local paths are intentionally out of scope here.
func DetectHarnessAccretion(home string, k HarnessKey) AccretionReport {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	switch k {
	case Claude:
		return AccretionReport{
			ExtraSkills:   extraSkillDirs(filepath.Join(home, ".claude", "skills")),
			ExtraCommands: commandFiles(filepath.Join(home, ".claude", "commands")),
		}
	case OpenCode:
		return AccretionReport{
			ExtraSkills: mergeSorted(
				prefixEntries("opencode", extraSkillDirs(filepath.Join(home, ".config", "opencode", "skills"))),
				prefixEntries("claude-compatible", extraSkillDirs(filepath.Join(home, ".claude", "skills"))),
				prefixEntries("agents-compatible", extraSkillDirs(filepath.Join(home, ".agents", "skills"))),
			),
			ExtraCommands: mergeSorted(
				prefixEntries("opencode/commands", commandFiles(filepath.Join(home, ".config", "opencode", "commands"))),
				prefixEntries("opencode/command", commandFiles(filepath.Join(home, ".config", "opencode", "command"))),
			),
		}
	case Codex:
		return AccretionReport{
			ExtraSkills: prefixEntries("codex", extraSkillDirs(filepath.Join(home, ".codex", "skills"))),
		}
	default:
		return AccretionReport{}
	}
}

// extraSkillDirs lists non-hidden skill dirs (containing SKILL.md) other
// than using-mcplexer.
func extraSkillDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || name == usingMcplexerSkillName {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// commandFiles lists non-hidden flat *.md command files.
func commandFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func prefixEntries(prefix string, entries []string) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, prefix+"/"+e)
	}
	return out
}

func mergeSorted(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		out = append(out, g...)
	}
	sort.Strings(out)
	return out
}
