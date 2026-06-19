package downstream

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MCP's stdio transport spawns whatever command/args a downstream config
// declares. OX Security's April 2026 disclosure showed that several major
// MCP host implementations leak this surface to network-reachable
// configuration endpoints, turning a misconfiguration into unauthenticated
// RCE. mcplexer requires an API token (post-2026-05 hardening) before any
// /api/v1/downstreams write, but defence-in-depth still pays:
//
//   - the API token may be exfiltrated via prompt-injection of an
//     already-trusted MCP server,
//   - a YAML typo (`command: rm`) shouldn't fail-open into RCE,
//   - addons / control-protocol can register downstreams without the same
//     human eyeballs as the dashboard.
//
// ValidateCommand rejects the obvious foot-guns: shells as the command
// field, shell-eval flags as arguments, and shell metacharacters anywhere
// in the command string. Set MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS=1 to
// bypass for users who genuinely need to run a shell as a downstream.

// shellCommandBasenames are interpreters that take arbitrary code via
// -c / -e and therefore should never be the "command" of a downstream
// config. Path-prefixed variants (e.g. /bin/sh) are also rejected.
var shellCommandBasenames = map[string]struct{}{
	"sh":             {},
	"bash":           {},
	"dash":           {},
	"zsh":            {},
	"ksh":            {},
	"ash":            {},
	"csh":            {},
	"tcsh":           {},
	"fish":           {},
	"eval":           {},
	"exec":           {},
	"source":         {},
	"cmd":            {},
	"cmd.exe":        {},
	"powershell":     {},
	"powershell.exe": {},
	"pwsh":           {},
}

// shellEvalArgs are flags that — when passed to *any* command — instruct
// the program to evaluate the next argument as code. node, python, ruby,
// php, perl, npx (-c via passthrough to a sub-shell), and the deno/bun
// runtimes all expose at least one of these. Allowing them on a
// downstream config would let a registered "node" runner execute
// arbitrary JS provided in args.
var shellEvalArgs = map[string]struct{}{
	"-c":              {},
	"-e":              {},
	"--call":          {},
	"--eval":          {},
	"--exec":          {},
	"--execute":       {},
	"--code":          {},
	"--inline":        {},
	"-Command":        {}, // PowerShell
	"-EncodedCommand": {},
}

// shellMetaChars are characters that have no business appearing in the
// "command" field. Args are intentionally not metachar-checked — many
// legitimate MCP servers want literal `>` etc. in argv.
const shellMetaChars = ";|&`$\n\r"

// protectedMcplexerPathFragments are substrings whose presence in any
// downstream command or arg disqualifies the spawn. These are mcplexer's
// own on-disk state — DB, encrypted backups, OAuth tokens, libp2p keys,
// API bearer — that no legitimate downstream MCP server has any reason
// to reference. The hook layer (~/.claude/hooks/block-mcplexer-db.sh)
// blocks the same fragments at the AI-tool-call layer; this is the
// gateway-side belt-and-braces in case a downstream config slips
// through review.
// The directory entries are stored WITHOUT a trailing slash so they match
// both a file beneath the dir (`.mcplexer/secrets/AGE_KEY`) AND a bare
// listing of the dir itself (`ls ~/.mcplexer/secrets`). A trailing-slash
// form would have let `ls ~/.mcplexer/secrets` (which enumerates every
// secret name) slip through. `.mcplexer/mcplexer.db` is a substring of
// `.mcplexer/mcplexer.db.age`, so the encrypted-backup variant is covered
// by the same entry.
var protectedMcplexerPathFragments = []string{
	".mcplexer/mcplexer.db",
	".mcplexer/api-key",
	".mcplexer/secrets",
	".mcplexer/p2p",
	".mcplexer/backups",
}

// ValidateCommand rejects spawn configurations that look like an attempt
// to execute arbitrary shell code. The check fails closed: an empty
// command, a relative path containing "..", or anything matching the
// shell allowlist returns an error.
//
// Returns nil for benign configs (e.g. "npx", "/usr/local/bin/uvx",
// "python3"). The MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS=1 escape hatch
// short-circuits validation entirely; intended for local scripting
// experiments, not production.
//
// Use this for DOWNSTREAM MCP-server registration paths only — places
// where an attacker-controlled config could turn into RCE if validation
// is loose. For agent-driven LOCAL Bash invocations (the
// /v1/hooks/pretool path) call ValidateLocalBashExec instead, which
// skips the interpreter + eval-flag checks because those false-positive
// on legitimate local commands like `bash /tmp/script.sh`,
// `grep -c PATTERN file`, `curl -c cookies.txt`, and `tar -c`.
func ValidateCommand(command string, args []string) error {
	if os.Getenv("MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS") == "1" {
		return nil
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("downstream command is empty")
	}

	if strings.ContainsAny(command, shellMetaChars) {
		return fmt.Errorf("downstream command contains shell metacharacters: %q", command)
	}

	// Reject command names that include parent-directory traversal. A
	// legitimate path may be absolute or single-name; `..` is never
	// necessary in an MCP server runner spec.
	if strings.Contains(command, "..") {
		return fmt.Errorf("downstream command contains path traversal: %q", command)
	}

	base := strings.ToLower(filepath.Base(command))
	if _, banned := shellCommandBasenames[base]; banned {
		return fmt.Errorf(
			"downstream command %q is a shell interpreter; "+
				"use a specific MCP runner (npx, uvx, node, python, …) instead, "+
				"or set MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS=1 to override",
			base,
		)
	}

	for _, a := range args {
		// Match the bare flag form ("-c") or the bundled form ("-c=…").
		flag, _, _ := strings.Cut(a, "=")
		if _, banned := shellEvalArgs[flag]; banned {
			return fmt.Errorf(
				"downstream command argument %q lets the runner evaluate arbitrary code; "+
					"refused as a defence against shell-injection in MCP configs. "+
					"Set MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS=1 to override",
				a,
			)
		}
		if frag, hit := containsProtectedFragment(a); hit {
			return fmt.Errorf(
				"downstream command argument %q references mcplexer protected path %q; "+
					"no legitimate downstream MCP server needs to touch the gateway's own state",
				a, frag,
			)
		}
	}
	if frag, hit := containsProtectedFragment(command); hit {
		return fmt.Errorf(
			"downstream command %q references mcplexer protected path %q",
			command, frag,
		)
	}

	return nil
}

// ValidateLocalBashExec is the lighter-touch sibling of ValidateCommand
// used by the /v1/hooks/pretool shell-hook path, where the "command" is
// not a downstream MCP-server registration but a one-off Bash
// invocation the user/agent is about to run locally.
//
// It applies ONLY the protected-mcplexer-path checks (so prompt
// injection can't talk the agent into reading the gateway DB, OAuth
// tokens, libp2p keys, secrets/, backups/ via Bash). It deliberately
// SKIPS:
//
//   - the shell-interpreter basename check (bash/sh/zsh/python/...).
//     `bash /tmp/script.sh` is a legitimate local invocation; the
//     downstream-config concern (operator misconfig → RCE) does not apply
//     when the human/agent is explicitly running a script locally.
//
//   - the eval-flag check (-c / -e / --eval / --exec / -Command / ...).
//     These argv tokens are false-positive on common local utilities:
//     `grep -c PATTERN file` (count), `curl -c cookies.txt` (jar),
//     `tar -c -f out.tar dir` (create), `gpg -c file` (symmetric encrypt).
//     The downstream-config concern (`node -e '<JS>'` as a registered
//     runner) does not apply when the harness owns the exec.
//
//   - the empty-command check (the hook path already pre-validates that
//     a Bash invocation has a non-empty command via extractBashCommand).
//
//   - the shell-metachar check on the executable. The shell-hook does its
//     own metachar check on the FULL command string in hooks_handler.go
//     (semicolons / pipes / backtick / newlines etc.) — that's the
//     load-bearing check; running it again on a single argv token is
//     redundant.
//
// The MCPLEXER_UNSAFE_DOWNSTREAM_COMMANDS=1 escape hatch is intentionally
// NOT honoured here — protected-path containment is a separate trust
// concern from "let me register a shell as a downstream runner". For a
// full bypass of every shell-hook guard the operator uses dangerous-mode
// at the manager layer.
func ValidateLocalBashExec(command string, args []string) error {
	if frag, hit := containsProtectedFragment(command); hit {
		return fmt.Errorf(
			"bash command %q references mcplexer protected path %q",
			command, frag,
		)
	}
	for _, a := range args {
		if frag, hit := containsProtectedFragment(a); hit {
			return fmt.Errorf(
				"bash command argument %q references mcplexer protected path %q",
				a, frag,
			)
		}
	}
	return nil
}

// containsProtectedFragment reports whether s mentions any of the
// mcplexer on-disk paths that downstream configs are forbidden from
// touching. Returns the matched fragment so the error message can
// identify which guard rule fired.
//
// The raw string is checked first (fast path). If that misses, the
// string is normalized via stripShellQuoting to defeat bypasses like
// sec”rets or se\crets that the shell evaluates to "secrets" before
// the kernel sees the path.
func containsProtectedFragment(s string) (string, bool) {
	for _, frag := range protectedMcplexerPathFragments {
		if strings.Contains(s, frag) {
			return frag, true
		}
	}
	normed := stripShellQuoting(s)
	if normed == s {
		return "", false
	}
	for _, frag := range protectedMcplexerPathFragments {
		if strings.Contains(normed, frag) {
			return frag, true
		}
	}
	return "", false
}

// stripShellQuoting removes one layer of shell quoting from s so that
// protected-path fragment matching can see through obfuscation like
// sec”rets (empty single-quote insertion), sec""rets (empty
// double-quote insertion), and se\crets (backslash escaping).
//
// This is NOT a full shell lexer. It handles three bypass patterns:
//
//   - Empty or non-empty single-quoted segments: the quote delimiters
//     are stripped and the content is emitted verbatim (no expansion
//     inside single quotes).
//   - Empty or non-empty double-quoted segments: the quote delimiters
//     are stripped; backslash escapes for $ ` " \ newline are resolved,
//     all other \X pairs pass through as-is.
//   - Backslash escapes outside quotes: the backslash is consumed and
//     the next byte is emitted literally.
//
// Known limitations (documented, acceptable for a defence-in-depth guard):
//   - $HOME / ~ expansion is NOT resolved; the guard already matches
//     .mcplexer/ fragments which are path-relative and unaffected.
//   - $() / backtick command substitution is NOT evaluated; the shell
//     metachar layer blocks those before this function is reached.
//   - Only one quoting layer is stripped; deeply nested quoting
//     (e.g. "'"'"'...) is not unfolded. An attacker who can inject
//     multi-level quoting can likely bypass the fragment check, but
//     the metachar guard blocks the quotes themselves in the
//     downstream-command path.
func stripShellQuoting(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	i := 0
	for i < len(s) {
		switch s[i] {
		case '\'':
			i++
			for i < len(s) && s[i] != '\'' {
				buf.WriteByte(s[i])
				i++
			}
			if i < len(s) {
				i++
			}
		case '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					switch s[i+1] {
					case '$', '`', '"', '\\', '\n':
						buf.WriteByte(s[i+1])
						i += 2
						continue
					}
				}
				buf.WriteByte(s[i])
				i++
			}
			if i < len(s) {
				i++
			}
		case '\\':
			if i+1 < len(s) {
				buf.WriteByte(s[i+1])
				i += 2
			} else {
				buf.WriteByte('\\')
				i++
			}
		default:
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}
