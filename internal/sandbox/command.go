package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PrepareCommandConfig grants the minimum per-invocation paths needed to
// launch program, work in workingDir, and use an isolated scratch directory.
// It never grants a home directory implicitly. Callers add provider-specific
// config/auth paths to base before calling this helper.
func PrepareCommandConfig(
	base Config, program, workingDir, scratchDir string, extraReadOnly ...string,
) Config {
	base.ReadOnlyPaths = append([]string(nil), base.ReadOnlyPaths...)
	base.ReadWritePaths = append([]string(nil), base.ReadWritePaths...)
	base.ReadOnlyPaths = append(base.ReadOnlyPaths, ExecutableReadPaths(program)...)
	base.ReadOnlyPaths = append(base.ReadOnlyPaths, extraReadOnly...)
	if workingDir != "" {
		base.ReadWritePaths = append(base.ReadWritePaths, workingDir)
		base.WorkingDir = workingDir
	}
	if scratchDir != "" {
		base.ReadWritePaths = append(base.ReadWritePaths, scratchDir)
	}
	base.ReadOnlyPaths = uniquePaths(base.ReadOnlyPaths)
	base.ReadWritePaths = uniquePaths(base.ReadWritePaths)
	return base
}

// ExecutableReadPaths resolves a command, its shebang interpreter, and the
// narrow package container used by common Homebrew, npm, and .app installs.
// This is read access only; process execution remains controlled by the
// platform profile and filesystem allowlist together.
func ExecutableReadPaths(program string) []string {
	seen := make(map[string]struct{})
	var out []string
	collectExecutableReadPaths(program, 0, seen, &out)
	return uniquePaths(out)
}

func collectExecutableReadPaths(program string, depth int, seen map[string]struct{}, out *[]string) {
	if program == "" || depth > 2 {
		return
	}
	if !filepath.IsAbs(program) {
		if resolved, err := exec.LookPath(program); err == nil {
			program = resolved
		} else {
			return
		}
	}
	program = filepath.Clean(program)
	if _, ok := seen[program]; ok {
		return
	}
	seen[program] = struct{}{}
	*out = append(*out, program)
	resolved := program
	if p, err := filepath.EvalSymlinks(program); err == nil {
		resolved = p
		*out = append(*out, p)
	}
	if root := executableRuntimeRoot(resolved); root != "" {
		*out = append(*out, root)
	}
	// A Homebrew-installed binary dynamically links sibling Homebrew
	// libraries (node -> libuv, icu, ...) via <prefix>/opt/<pkg>/lib and
	// <prefix>/lib. Cellar-package-root grants alone leave those blocked,
	// so dyld aborts ("Library not loaded ... blocked by sandbox"). Grant
	// the package-manager library roots read-only — public shared
	// objects, never credentials (which the deny-list covers separately).
	*out = append(*out, packageManagerLibraryRoots(resolved)...)
	if interpreter := shebangInterpreter(resolved); interpreter != "" {
		collectExecutableReadPaths(interpreter, depth+1, seen, out)
	}
}

// packageManagerLibraryRoots returns the shared-library search roots for
// a binary installed under a Homebrew (or Linuxbrew) prefix, so its
// dynamic dependencies resolve inside the sandbox. Detects the prefix
// from a "/Cellar/" or "/opt/" segment in the resolved path. Returns nil
// for binaries outside a recognized prefix.
func packageManagerLibraryRoots(resolved string) []string {
	sep := string(os.PathSeparator)
	parts := strings.Split(filepath.Clean(resolved), sep)
	prefixEnd := -1
	for i, part := range parts {
		// "Cellar" unambiguously marks a brew prefix (the segment before
		// it). The opt-symlink form (<prefix>/opt/<pkg>/...) is caught by
		// the explicit brew-directory markers so the leading system /opt
		// (prefix "") is not mistaken for one.
		if part == "Cellar" && i > 0 {
			prefixEnd = i
			break
		}
		if (part == "homebrew" || part == ".linuxbrew") && i > 0 {
			prefixEnd = i + 1
			break
		}
	}
	if prefixEnd <= 0 {
		return nil
	}
	prefix := strings.Join(parts[:prefixEnd], sep)
	if prefix == "" {
		return nil
	}
	return []string{
		filepath.Join(prefix, "opt"), // per-package lib symlinks
		filepath.Join(prefix, "lib"), // flattened dylibs
		// The opt/<pkg> symlinks resolve into Cellar, and sandbox-exec
		// checks the RESOLVED path — so granting opt/ alone is not enough
		// for a dylib at opt/libuv/lib -> Cellar/libuv/VER/lib. All
		// Cellar contents are public shared software, never credentials.
		filepath.Join(prefix, "Cellar"),
		// Package config read by linked libraries at runtime, e.g. node's
		// OpenSSL reads etc/openssl@3/openssl.cnf ("BIO_new_file:
		// Operation not permitted" when denied). Public config only.
		filepath.Join(prefix, "etc"),
	}
}

func executableRuntimeRoot(path string) string {
	parts := strings.Split(filepath.Clean(path), string(os.PathSeparator))
	for i, part := range parts {
		switch {
		case part == "Cellar" && i+2 < len(parts):
			return strings.Join(parts[:i+3], string(os.PathSeparator))
		case part == "node_modules" && i+1 < len(parts):
			end := i + 2
			if strings.HasPrefix(parts[i+1], "@") && i+2 < len(parts) {
				end++
			}
			return strings.Join(parts[:end], string(os.PathSeparator))
		case strings.HasSuffix(part, ".app") && i+1 < len(parts) && parts[i+1] == "Contents":
			return strings.Join(parts[:i+2], string(os.PathSeparator))
		}
	}
	return ""
}

func shebangInterpreter(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 {
		return ""
	}
	if fields[0] != "/usr/bin/env" {
		return fields[0]
	}
	for _, field := range fields[1:] {
		if !strings.HasPrefix(field, "-") {
			return field
		}
	}
	return ""
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

var commandEnvironmentBaseAllowlist = map[string]struct{}{
	"PATH":                    {},
	"HOME":                    {},
	"USER":                    {},
	"LOGNAME":                 {},
	"SHELL":                   {},
	"TERM":                    {},
	"COLORTERM":               {},
	"NO_COLOR":                {},
	"FORCE_COLOR":             {},
	"LANG":                    {},
	"LANGUAGE":                {},
	"LC_ALL":                  {},
	"LC_CTYPE":                {},
	"LC_COLLATE":              {},
	"LC_MESSAGES":             {},
	"LC_MONETARY":             {},
	"LC_NUMERIC":              {},
	"LC_TIME":                 {},
	"LC_PAPER":                {},
	"LC_NAME":                 {},
	"LC_ADDRESS":              {},
	"LC_TELEPHONE":            {},
	"LC_MEASUREMENT":          {},
	"LC_IDENTIFICATION":       {},
	"TZ":                      {},
	"__CF_USER_TEXT_ENCODING": {},
	"HTTP_PROXY":              {},
	"HTTPS_PROXY":             {},
	"ALL_PROXY":               {},
	"NO_PROXY":                {},
	"http_proxy":              {},
	"https_proxy":             {},
	"all_proxy":               {},
	"no_proxy":                {},
	"SSL_CERT_FILE":           {},
	"SSL_CERT_DIR":            {},
	"NODE_EXTRA_CA_CERTS":     {},
}

// AllowlistedCommandEnvironment rebuilds a child environment from a closed
// set of OS/runtime essentials plus exact caller-approved keys. HOME is kept
// for standard CLI config discovery; filesystem policy must still grant only
// the specific config paths that child needs. Temp-directory variables are
// never inherited and always point at the per-invocation directory.
//
// Additional keys are exact matches: suffixes such as _TOKEN, _SECRET,
// _PASSWORD, and _KEY do not gain access unless a caller names the complete
// provider variable explicitly.
func AllowlistedCommandEnvironment(base []string, dir string, additionalKeys ...string) []string {
	allowed := make(map[string]struct{}, len(commandEnvironmentBaseAllowlist)+len(additionalKeys))
	for key := range commandEnvironmentBaseAllowlist {
		allowed[key] = struct{}{}
	}
	for _, key := range additionalKeys {
		if key != "" {
			allowed[key] = struct{}{}
		}
	}

	out := make([]string, 0, len(allowed)+3)
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "TMPDIR" || key == "TMP" || key == "TEMP" {
			continue
		}
		if _, ok := allowed[key]; ok {
			out = append(out, entry)
		}
	}
	if dir != "" {
		out = append(out, "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)
	}
	return out
}

// CommandEnvironmentReadPaths returns the existing absolute CA certificate
// files and directories named by the exact network-runtime variables admitted
// by AllowlistedCommandEnvironment. Callers grant these paths read-only so a
// CLI using a private CA can still establish TLS without exposing unrelated
// host files. Relative and missing paths are ignored.
func CommandEnvironmentReadPaths(base []string) []string {
	var paths []string
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		switch key {
		case "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS":
			paths = appendExistingAbsolutePath(paths, value)
		case "SSL_CERT_DIR":
			for _, path := range filepath.SplitList(value) {
				paths = appendExistingAbsolutePath(paths, path)
			}
		}
	}
	return uniquePaths(paths)
}

func appendExistingAbsolutePath(paths []string, path string) []string {
	if !filepath.IsAbs(path) {
		return paths
	}
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err != nil {
		return paths
	}
	paths = append(paths, path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		paths = append(paths, resolved)
	}
	return paths
}
