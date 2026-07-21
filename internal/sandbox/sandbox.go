// Package sandbox isolates AI-driven processes from the host filesystem
// and network. On Linux/Pi it uses bubblewrap (or unshare as a fallback);
// on macOS it uses sandbox-exec; on Windows it returns ErrUnsupportedOS.
//
// All drivers expose the same Driver interface so the higher-level
// "guards" layer can compose Sandbox + Shell Guard + Approval Manager
// without caring which kernel mechanism is in play underneath.
package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ProxySocketEnvVar is the env var that points at the mcplexer-proxy UDS
// socket path. When set and NonEmpty, the sandbox driver can allow egress
// through the proxy instead of blocking it. Empty (default) makes
// NetworkProxy behave identically to NetworkDeny — fail-closed.
const ProxySocketEnvVar = "MCPLEXER_PROXY_SOCKET"

// ErrUnsupportedOS is returned by SelectDriver / Driver.Run when no
// usable sandbox mechanism is available on the current host (e.g.
// Windows, or Linux without bwrap or unshare).
var ErrUnsupportedOS = errors.New("sandbox: no driver available for this OS")

// Network policy constants. Stringly-typed because they map 1:1 to the
// JSON serialization the dashboard uses; introducing an enum here forces
// a translation layer we don't currently need.
const (
	NetworkDeny  = "deny"
	NetworkProxy = "proxy"
	NetworkHost  = "host"
)

// DefaultWorkingDir is the CWD a sandboxed process starts in when
// cfg.WorkingDir is unset. Kept consistent with the Guards plan so the
// admin MCP tools stay CWD-gate-hidden by default (the admin gate
// requires CWD under ~/.mcplexer, which "/workspace" is not).
const DefaultWorkingDir = "/workspace"

// Config describes the bind-mount + network policy for a sandbox.
// Zero-value Config is safe (most-restrictive defaults).
type Config struct {
	// ReadOnlyPaths are mounted into the sandbox read-only. Typical
	// entries: the source repo, ~/.config/<client> (config files).
	ReadOnlyPaths []string

	// ReadWritePaths are mounted into the sandbox read-write. Typical
	// entries: the source repo (so the agent can edit), a scratch dir.
	ReadWritePaths []string

	// DenyPaths are explicitly NOT mounted, even if they sit under a
	// parent that would otherwise be visible. Defaults always include
	// ~/.ssh, ~/.mcplexer, ~/.aws, ~/.docker/config.json,
	// /var/run/docker.sock — append, never replace.
	DenyPaths []string

	// DenyWritePaths are read-allowed but write-denied. The use case is
	// credential dirs the child process LEGITIMATELY needs to read (e.g.
	// `~/.claude/.credentials.json` for OAuth) but must not be able to
	// mutate (e.g. install a malicious `~/.claude/settings.json`
	// PreToolUse hook via prompt injection).
	//
	// On the darwin sandbox-exec driver this renders as an explicit
	// subtree read allow followed by `(deny file-write* ...)`. On
	// bwrap/Linux this maps to a read-only bind mount.
	DenyWritePaths []string

	// WorkingDir is the CWD inside the sandbox. Defaults to "/workspace"
	// (kept consistent with the Guards plan so admin MCP tools stay
	// CWD-gate-hidden by default).
	WorkingDir string

	// Network is the egress policy. Empty and "deny" block all outbound;
	// "proxy" forces all egress through mcplexer-proxy on UDS;
	// "host" inherits the host's network unchanged (development only).
	Network string

	// ProxySocket is the UDS path for the mcplexer-proxy MITM daemon.
	// Only consulted when Network == "proxy"; when set and the socket
	// exists, egress is routed through the proxy. When empty, proxy
	// mode falls back to deny (fail-closed). The default value is read
	// from the MCPLEXER_PROXY_SOCKET env var.
	ProxySocket string

	// AllowSudo, when true, routes sudo invocations through the
	// privileged helper. When false, sudo just exits non-zero inside the
	// sandbox.
	AllowSudo bool
}

// ProxySocketFromEnv returns the MCPLEXER_PROXY_SOCKET value, or "" when
// unset. Exported so sandbox config builders can default the field.
func ProxySocketFromEnv() string {
	return os.Getenv(ProxySocketEnvVar)
}

// ErrProxyNotConfigured is returned by ValidateConfig when Network=proxy
// but no ProxySocket is set.
var ErrProxyNotConfigured = errors.New("sandbox: Network=proxy requires ProxySocket (set MCPLEXER_PROXY_SOCKET)")

// ValidateConfig checks that the sandbox Config is internally consistent.
// Returns nil for a valid config; callers should validate before passing
// cfg to Driver.Run, especially when Network=proxy without a socket path.
// A zero-value Config is always valid (most restrictive defaults).
func ValidateConfig(cfg Config) error {
	if cfg.Network == NetworkProxy && cfg.ProxySocket == "" {
		return ErrProxyNotConfigured
	}
	return nil
}

// ExitCode mirrors the wrapped process's exit status (0..255 on POSIX).
// A separate named type makes the Driver.Run signature self-documenting
// and forces callers to think about it (vs. swallowing a bare int).
type ExitCode int

// Driver runs a single command inside an isolated environment.
type Driver interface {
	// Name returns a stable driver identifier ("sandbox-exec", "bwrap",
	// "unshare"). Used in audit + UI.
	Name() string

	// Available reports whether this driver can be used on the current
	// host. macOS sandbox-exec returns true only on darwin; bubblewrap
	// returns true only if `bwrap` resolves on $PATH.
	Available() bool

	// Run executes program with args inside a sandbox built from cfg.
	// The returned ExitCode mirrors the wrapped process's exit. Network,
	// capture, and timeout are all controlled via cfg + ctx.
	Run(ctx context.Context, cfg Config, program string, args []string) (ExitCode, error)
}

// DefaultDenyPaths returns the canonical paths that are NEVER bind-mounted
// into a sandbox, regardless of Config.ReadOnlyPaths/ReadWritePaths. Use
// this to seed Config.DenyPaths.
//
// The list is intentionally short — these are the credentials and
// privileged sockets that a compromised agent could weaponize most
// directly. Anything more nuanced (cloud-CLI tokens, GPG keyrings) is a
// per-driver concern.
func DefaultDenyPaths(home string) []string {
	if home == "" {
		// Caller forgot to resolve $HOME. Returning an empty list would
		// silently strip the safety net, so return the absolute paths
		// that don't depend on home and let the caller notice if any
		// expected entry is missing.
		return []string{
			"/var/run/docker.sock",
		}
	}
	return []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".mcplexer"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".docker", "config.json"),
		"/var/run/docker.sock",
	}
}

// MergeDenyPaths returns the union of DefaultDenyPaths(home) and
// extra, preserving order (defaults first) and de-duplicating. Drivers
// call this when building their per-invocation deny list so users who
// pass an explicit DenyPaths can extend rather than replace the safety
// net.
func MergeDenyPaths(home string, extra []string) []string {
	base := DefaultDenyPaths(home)
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, p := range base {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range extra {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
