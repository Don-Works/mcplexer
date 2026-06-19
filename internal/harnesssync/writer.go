package harnesssync

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

// Marker regexes for our harness-scoped blocks (versioned begin, harness in
// paren). Grok's target is TOML and therefore uses # comments. The HTML regex
// is intentionally still recognized for Grok so a fixed sync can clean up
// older invalid ~/.grok/config.toml files.
var (
	harnessHTMLMarkerRegex = regexp.MustCompile(`(?ms)^<!-- MCPLEXER:HARNESS-SYNC:BEGIN v(\d+) \(([a-z-]+)\) -->\s*\n(.*?)\n?<!-- MCPLEXER:HARNESS-SYNC:END -->\s*$`)
	harnessTOMLMarkerRegex = regexp.MustCompile(`(?ms)^# MCPLEXER:HARNESS-SYNC:BEGIN v(\d+) \(([a-z-]+)\)\s*\n(.*?)\n?# MCPLEXER:HARNESS-SYNC:END\s*$`)
)

// Install writes (or refreshes) the harness bootstrap artifact(s) for key.
// It preserves existing file content and inserts/replaces only the MCPLEXER:HARNESS-SYNC
// managed block, coexisting with older MCPLEXER rules/managed blocks.
// Returns changed, the resulting status snapshot (file based), and any err.
// It never touches real ~/.mcplexer; paths are derived from provided home
// (tests pass t.TempDir()).
func Install(home string, k HarnessKey, regVersion int) (changed bool, st HarnessStatus, err error) {
	if !Valid(k) {
		return false, st, fmt.Errorf("unknown harness key: %s", k)
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	sidecarChanged := false
	if k == Claude {
		sidecarChanged, err = ensureClaudeSkillSidecar(home)
		if err != nil {
			return false, st, err
		}
	}
	if k == OpenCode {
		sidecarChanged, err = ensureOpenCodeSkillSidecar(home)
		if err != nil {
			return false, st, err
		}
	}
	target := TargetPath(home, k)
	rendered := Render(k, regVersion)
	renderedBlock, _ := extractHarnessBlock(rendered, k)

	cur, readErr := os.ReadFile(target)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, st, fmt.Errorf("read %s: %w", target, readErr)
	}

	var newContent string
	if readErr == nil {
		// existing file: preserve content, insert/replace our block only
		if curBlock, ok := extractHarnessBlock(string(cur), k); ok {
			curRawBlock, _ := extractHarnessRawBlock(string(cur), k)
			if normalizeHashBody(curBlock) == normalizeHashBody(renderedBlock) &&
				normalizeHashBody(curRawBlock) == normalizeHashBody(rendered) {
				// already good
				st = fileStatus(target, k, regVersion, false)
				return sidecarChanged, st, nil
			}
			// replace existing block
			newContent = replaceHarnessBlock(string(cur), k, rendered)
		} else {
			// no existing block for this harness: append our block, preserve rest
			newContent = string(cur) + "\n" + rendered
		}
	} else {
		// new file: write just the rendered content (no existing content to preserve)
		newContent = rendered
	}

	// write (or create parent)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return false, st, fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(target, []byte(newContent), 0o600); err != nil {
		return false, st, fmt.Errorf("write: %w", err)
	}

	st = fileStatus(target, k, regVersion, false)
	st.BootstrapInstalled = true
	st.BootstrapVersion = intPtr(regVersion)
	st.RegistryVersion = regVersion
	return true, st, nil
}

// fileStatus builds a HarnessStatus by inspecting the on-disk target.
func fileStatus(target string, k HarnessKey, regVersion int, drifted bool) HarnessStatus {
	var ver *int
	if target != "" && fileExists(target) {
		if data, err := os.ReadFile(target); err == nil {
			if v, ok := parseHarnessVersion(string(data), k); ok {
				ver = &v
			}
		}
	}
	return HarnessStatus{
		Key:                string(k),
		BootstrapInstalled: ver != nil,
		BootstrapVersion:   ver,
		RegistryVersion:    regVersion,
		Drifted:            drifted,
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func intPtr(i int) *int { return &i }

// extractHarnessBlock returns the body between our markers for the key, if present.
func extractHarnessBlock(s string, k HarnessKey) (string, bool) {
	for _, match := range harnessBlockMatches(s) {
		if match.key == k {
			return match.body, true
		}
	}
	return "", false
}

// replaceHarnessBlock replaces the existing harness block for key with rendered.
// Returns the full file content with the block replaced.
func replaceHarnessBlock(s string, k HarnessKey, rendered string) string {
	replaced := false
	for _, pattern := range harnessReplacePatterns(k) {
		re := regexp.MustCompile(pattern)
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, rendered)
			replaced = true
		}
	}
	if replaced {
		return s
	}
	return s
}

func parseHarnessVersion(s string, k HarnessKey) (int, bool) {
	for _, match := range harnessBlockMatches(s) {
		if match.key == k {
			return match.version, true
		}
	}
	return 0, false
}

type harnessBlockMatch struct {
	version int
	key     HarnessKey
	body    string
	raw     string
}

func harnessBlockMatches(s string) []harnessBlockMatch {
	var out []harnessBlockMatch
	for _, sub := range harnessHTMLMarkerRegex.FindAllStringSubmatch(s, -1) {
		if len(sub) == 4 {
			out = append(out, harnessBlockMatch{
				version: atoi(sub[1]),
				key:     HarnessKey(sub[2]),
				body:    sub[3],
				raw:     sub[0],
			})
		}
	}
	for _, sub := range harnessTOMLMarkerRegex.FindAllStringSubmatch(s, -1) {
		if len(sub) == 4 {
			out = append(out, harnessBlockMatch{
				version: atoi(sub[1]),
				key:     HarnessKey(sub[2]),
				body:    uncommentTOMLBody(sub[3]),
				raw:     sub[0],
			})
		}
	}
	return out
}

func extractHarnessRawBlock(s string, k HarnessKey) (string, bool) {
	for _, match := range harnessBlockMatches(s) {
		if match.key == k {
			return match.raw, true
		}
	}
	return "", false
}

func harnessReplacePatterns(k HarnessKey) []string {
	quoted := regexp.QuoteMeta(string(k))
	return []string{
		fmt.Sprintf(`(?ms)^<!-- MCPLEXER:HARNESS-SYNC:BEGIN v\d+ \(%s\) -->.*?^<!-- MCPLEXER:HARNESS-SYNC:END -->`, quoted),
		fmt.Sprintf(`(?ms)^# MCPLEXER:HARNESS-SYNC:BEGIN v\d+ \(%s\)\s*\n.*?^# MCPLEXER:HARNESS-SYNC:END`, quoted),
	}
}

func uncommentTOMLBody(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "#" {
			out = append(out, "")
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			out = append(out, strings.TrimPrefix(trimmed, "# "))
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			out = append(out, strings.TrimPrefix(trimmed, "#"))
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Recheck inspects the live file(s) and returns a status with drifted set
// if the installed block hash does not match the current Render hash.
func Recheck(home string, k HarnessKey, regVersion int) (HarnessStatus, error) {
	if !Valid(k) {
		return HarnessStatus{Key: string(k), RegistryVersion: regVersion}, fmt.Errorf("unknown harness key: %s", k)
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	target := TargetPath(home, k)
	st := fileStatus(target, k, regVersion, false)
	if acc := DetectHarnessAccretion(home, k); !acc.Empty() {
		st.Accretion = &acc
	}
	if !st.BootstrapInstalled {
		st.Drifted = false
		return st, nil
	}
	data, err := os.ReadFile(target)
	if err != nil {
		st.Drifted = true
		return st, nil
	}
	got, ok := extractHarnessBlock(string(data), k)
	if !ok {
		st.BootstrapInstalled = false
		st.BootstrapVersion = nil
		st.Drifted = false
		return st, nil
	}
	want, _ := extractHarnessBlock(Render(k, regVersion), k)
	if normalizeHashBody(got) != normalizeHashBody(want) {
		st.Drifted = true
	}
	if k == Claude && claudeSkillSidecarDrifted(home) {
		st.Drifted = true
	}
	if k == OpenCode && openCodeSkillSidecarDrifted(home) {
		st.Drifted = true
	}
	return st, nil
}

func ensureClaudeSkillSidecar(home string) (bool, error) {
	skillBody, err := skillregistry.SeedBody(usingMcplexerSkillName)
	if err != nil {
		return false, fmt.Errorf("using-mcplexer seed: %w", err)
	}
	skillPath := ClaudeSkillPath(home)
	if cur, err := os.ReadFile(skillPath); err == nil && string(cur) == skillBody {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir skill: %w", err)
	}
	if err := os.WriteFile(skillPath, []byte(skillBody), 0o644); err != nil {
		return false, fmt.Errorf("write skill: %w", err)
	}
	return true, nil
}

func claudeSkillSidecarDrifted(home string) bool {
	skillBody, err := skillregistry.SeedBody(usingMcplexerSkillName)
	if err != nil {
		return true
	}
	cur, err := os.ReadFile(ClaudeSkillPath(home))
	return err != nil || string(cur) != skillBody
}

func ensureOpenCodeSkillSidecar(home string) (bool, error) {
	skillBody, err := skillregistry.SeedBody(usingMcplexerSkillName)
	if err != nil {
		return false, fmt.Errorf("using-mcplexer seed: %w", err)
	}
	skillPath := OpenCodeSkillPath(home)
	if cur, err := os.ReadFile(skillPath); err == nil && string(cur) == skillBody {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		return false, fmt.Errorf("mkdir skill: %w", err)
	}
	if err := os.WriteFile(skillPath, []byte(skillBody), 0o644); err != nil {
		return false, fmt.Errorf("write skill: %w", err)
	}
	return true, nil
}

func openCodeSkillSidecarDrifted(home string) bool {
	skillBody, err := skillregistry.SeedBody(usingMcplexerSkillName)
	if err != nil {
		return true
	}
	cur, err := os.ReadFile(OpenCodeSkillPath(home))
	return err != nil || string(cur) != skillBody
}
