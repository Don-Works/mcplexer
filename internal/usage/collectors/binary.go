package collectors

import (
	"os"
	"os/exec"
	"path/filepath"
)

// ResolveBinary finds a supported usage CLI even when launchd supplies a
// minimal PATH. It returns the bare name when no executable is installed so
// callers still receive the normal, useful exec error.
func ResolveBinary(name string) string {
	return resolveBinary(name, binaryCandidates(name))
}

func resolveBinary(name string, candidates []string) string {
	if resolved, err := exec.LookPath(name); err == nil {
		return resolved
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return name
}

func binaryCandidates(name string) []string {
	home, _ := os.UserHomeDir()
	homePath := func(parts ...string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, parts...)...)
	}
	common := []string{
		homePath(".local", "bin", name),
		homePath("bin", name),
		filepath.Join("/opt/homebrew/bin", name),
		filepath.Join("/usr/local/bin", name),
	}
	switch name {
	case "claude":
		return append([]string{homePath(".claude", "local", "claude")}, common...)
	case "codex":
		return append([]string{
			homePath(".codex", "bin", "codex"),
			"/Applications/Codex.app/Contents/Resources/codex",
		}, common...)
	case "grok":
		return append([]string{
			homePath(".grok", "bin", "grok"),
			homePath(".grok", "downloads", "grok-macos-aarch64"),
			"/Applications/cmux.app/Contents/Resources/bin/grok",
		}, common...)
	case "opencode":
		return append([]string{homePath(".opencode", "bin", "opencode")}, common...)
	case "mimo":
		return append([]string{
			homePath(".mimo", "bin", "mimo"),
			homePath(".mimocode", "bin", "mimo"),
			homePath(".bun", "bin", "mimo"),
		}, common...)
	default:
		return common
	}
}
