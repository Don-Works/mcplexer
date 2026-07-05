//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

var sandboxSafeEnv = []string{
	"PATH", "HOME", "USER", "SHELL", "TERM", "LANG", "TMPDIR", "TMP",
}

// sandboxExecPath is the absolute path to Apple's sandbox-exec binary.
// Hard-coded rather than going through exec.LookPath: on macOS this is
// part of the base system and a substituted /usr/local/bin/sandbox-exec
// would be a meaningful security regression.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// sandboxExecDriver implements Driver using Apple's sandbox-exec(1) and
// a generated TinyScheme .sb profile. The profile starts permissive
// ("allow default") and adds explicit denies for sensitive paths plus
// any caller-supplied DenyPaths. A full deny-by-default profile is its
// own project; this is a starting profile that meaningfully reduces
// blast radius without breaking everyday developer tooling.
type sandboxExecDriver struct{}

func (d *sandboxExecDriver) Name() string    { return "sandbox-exec" }
func (d *sandboxExecDriver) Available() bool { return runtime.GOOS == "darwin" }

// Run materializes a sandbox profile to a tempfile, execs sandbox-exec
// with that profile + the caller's program, wires stdio through, and
// returns the wrapped exit code transparently. Profile file is removed
// on return (defer) regardless of how the child exited.
func (d *sandboxExecDriver) Run(
	ctx context.Context, cfg Config, program string, args []string,
) (ExitCode, error) {
	if program == "" {
		return -1, errors.New("sandbox: program is required")
	}

	home, _ := os.UserHomeDir()
	profile := buildSandboxExecProfile(cfg, home)

	pf, err := writeProfileTemp(profile)
	if err != nil {
		return -1, err
	}
	defer func() { _ = os.Remove(pf) }()

	argv := append([]string{"-f", pf, program}, args...)
	cmd := exec.CommandContext(ctx, sandboxExecPath, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = cleanSandboxEnv(sandboxSafeEnv)
	if cfg.WorkingDir != "" {
		cmd.Dir = cfg.WorkingDir
	}

	err = cmd.Run()
	if cmd.ProcessState != nil {
		return ExitCode(cmd.ProcessState.ExitCode()), filterRunErr(err)
	}
	return -1, err
}

func cleanSandboxEnv(safeVars []string) []string {
	safe := make(map[string]struct{}, len(safeVars))
	for _, k := range safeVars {
		safe[k] = struct{}{}
	}
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if _, ok := safe[k]; ok {
			out = append(out, e)
		}
	}
	return out
}

// writeProfileTemp writes the .sb profile string to a fresh tempfile
// (0600) and returns the path. Caller is responsible for removing it.
func writeProfileTemp(profile string) (string, error) {
	f, err := os.CreateTemp("", "mcplexer-sandbox-*.sb")
	if err != nil {
		return "", fmt.Errorf("create sandbox profile: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := os.Chmod(f.Name(), 0600); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	if _, err := f.WriteString(profile); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write sandbox profile: %w", err)
	}
	return f.Name(), nil
}

// buildSandboxExecProfile renders the TinyScheme profile. We start
// permissive ("allow default"), then layer deny rules for the
// credentials directories every dev box has, then the caller's
// DenyPaths, then optionally network. NetworkProxy is fail-closed —
// when ProxySocket is empty (the default) it behaves identically to
// NetworkDeny; when ProxySocket is set the driver would allow egress
// through the mcplexer-proxy UDS (not yet implemented — see
// wrap_darwin.go for the staging path and internal/sandbox/ for the
// proxy UDS MITM daemon PR).
func buildSandboxExecProfile(cfg Config, home string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")

	// Hard-coded credential denies expressed as regex so they catch any
	// user under /Users/. Belt-and-braces alongside the subtree deny
	// paths we emit below from MergeDenyPaths(home, cfg.DenyPaths).
	// file-write-create, file-write-unlink, and file-write-mode are
	// separate operations in Apple's sandbox-exec profile language —
	// denying only file-write-data left a hole where a sandboxed process
	// could create new files, delete existing ones, or chmod under the
	// denied subtree.
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode (regex #"^/Users/[^/]+/\.ssh/"))` + "\n")
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode (regex #"^/Users/[^/]+/\.aws/"))` + "\n")
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode (regex #"^/Users/[^/]+/\.docker/config\.json$"))` + "\n")

	for _, p := range MergeDenyPaths(home, cfg.DenyPaths) {
		b.WriteString(denySubtreeLine(p))
	}

	// DenyWritePaths: read-allow + write-deny. Renders as a subtree-wide
	// deny on file-write-data so any path UNDER the listed prefix is
	// write-blocked (e.g. `~/.claude` denies `~/.claude/settings.json`
	// too). We use a regex anchored at the prefix because (literal "...")
	// only matches the exact path — settings files live deeper.
	for _, p := range cfg.DenyWritePaths {
		if p == "" {
			continue
		}
		b.WriteString(denyWriteSubtreeLine(p))
	}

	switch cfg.Network {
	case NetworkDeny:
		b.WriteString("(deny network*)\n")
	case NetworkProxy:
		// TODO(guards/m3): route egress through mcplexer-proxy UDS once
		// the MITM proxy lands. For now treat proxy === deny so callers
		// who request "proxy" fail closed rather than open (even when
		// the socket path is supplied — the proxy daemon doesn't exist
		// yet, so an allow rule would let egress through unchecked).
		b.WriteString("(deny network*)\n")
	case NetworkHost, "":
		// No network rule; inherit host network.
	}

	return b.String()
}

// denySubtreeLine emits one deny rule covering file-read-data,
// file-write-data, file-write-create, file-write-unlink, and
// file-write-mode under (subpath "<quoted>"). subpath matches the path
// AND every descendant, so a directory deny (e.g. ~/.mcplexer) covers
// ~/.mcplexer/mcplexer.db, ~/.mcplexer/secrets/*, ~/.mcplexer/p2p/*,
// ~/.mcplexer/api-key, etc. A plain (literal ...) matched only the
// directory inode itself and left every file beneath it fully
// readable+writable — the exact hole this closes (a prompt-injected
// claude_cli/opencode_cli worker could otherwise exfiltrate the gateway
// DB, age secret store, and mesh identity keys despite the deny rule).
// The create/unlink/mode operations are necessary because Apple's
// sandbox-exec treats file-write-create (creating new files),
// file-write-unlink (deleting files), and file-write-mode (chmod) as
// separate permission checks from file-write-data (writing to an
// existing file's content). Omitting them left holes where a sandboxed
// process could create, delete, or chmod files under denied subtrees.
// For a regular-file deny path (e.g. ~/.docker/config.json) subpath is
// equivalent to literal. strconv.Quote handles embedded backslashes and
// double-quotes — sandbox-exec accepts Go-style \-escapes.
func denySubtreeLine(p string) string {
	q := strconv.Quote(p)
	return "(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode (subpath " + q + "))\n"
}

// denyWriteSubtreeLine emits a write-only deny anchored at p, covering
// p itself AND any path beneath it. Read access is left untouched so
// callers that legitimately need to READ a credentials file inside the
// subtree (e.g. claude_cli reading ~/.claude/.credentials.json for
// OAuth) can still do so while writes — including PreToolUse hook
// installations into ~/.claude/settings.json — fail closed.
//
// All three write mutations are denied: file-write-data (modify
// existing content), file-write-create (new files/dirs), file-write-unlink
// (delete), and file-write-mode (chmod). Without these, a sandboxed
// process could create, delete, or chmod files under the write-denied
// subtree even though file-write-data was blocked.
//
// sandbox-exec's TinyScheme regex form is `(regex #"...")` where the
// quoted body is interpreted as a regex pattern directly — we therefore
// emit the pattern as a raw string, escaping only the regex
// metacharacters in p (`.`, `+`, etc.) and double-quotes (which would
// terminate the string). The hard-coded credential denies above use
// the same shape (`#"^/Users/[^/]+/\.ssh/"`).
func denyWriteSubtreeLine(p string) string {
	p = strings.TrimRight(p, "/")
	pattern := "^" + regexEscapeForSBPL(p) + "(/|$)"
	return "(deny file-write-data file-write-create file-write-unlink file-write-mode (regex #\"" + pattern + "\"))\n"
}

// regexEscapeForSBPL escapes the regex metacharacters that legitimately
// appear in absolute home-relative paths (`.`, `+`) so they match
// literally inside the sandbox-exec regex. Slashes and alphanumerics
// pass through unchanged. We intentionally do NOT call regexp.QuoteMeta
// because that escapes characters (e.g. `(`, `)`) sandbox-exec's
// regex dialect does not recognise the same way. Double-quote is
// escaped too because the body of `(regex #"…")` ends at the next
// unescaped quote.
func regexEscapeForSBPL(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\', '"':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
