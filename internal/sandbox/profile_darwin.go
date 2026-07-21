//go:build darwin

package sandbox

import (
	"path/filepath"
	"strconv"
	"strings"
)

// darwinRuntimeReadPaths are immutable OS paths needed to start ordinary
// command-line programs. Third-party runtimes (Homebrew, npm, model CLIs,
// and repositories) intentionally are not implicit; callers list them in
// ReadOnlyPaths or ReadWritePaths.
var darwinRuntimeReadPaths = []string{
	"/Library/Apple",
	"/System",
	"/bin",
	"/sbin",
	"/usr/bin",
	"/usr/lib",
	"/usr/libexec",
	"/usr/sbin",
	"/usr/share",
	"/private/var/db/timezone",
	"/private/var/select",
	"/private/etc/ssl",
}

var darwinRuntimeReadLiterals = []string{
	"/", "/etc", "/tmp", "/var",
	"/private/etc/localtime",
	"/private/etc/passwd",
	"/private/etc/protocols",
	"/private/etc/hosts",
	"/private/etc/resolv.conf",
	"/private/etc/services",
	"/private/var/run/resolv.conf",
	"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom",
}

// buildSandboxExecProfile renders a deny-by-default TinyScheme profile.
// The fixed rules only bootstrap platform binaries and libc. Every
// non-system path and host-network grant comes from Config explicitly.
func buildSandboxExecProfile(cfg Config, home string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	writeDarwinProcessRuntime(&b)
	writeDarwinSystemFiles(&b)
	writeDarwinConfiguredFiles(&b, cfg)
	writeDarwinCredentialDenies(&b, home, cfg.DenyPaths)
	writeDarwinNetworkPolicy(&b, cfg.Network)
	return b.String()
}

func writeDarwinProcessRuntime(b *strings.Builder) {
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow signal (target self))\n")
	b.WriteString("(allow process-info* (target self))\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString(`(allow ipc-posix-shm-read* (ipc-posix-name "apple.shm.notification_center") (ipc-posix-name-prefix "apple.cfprefs."))` + "\n")
	b.WriteString(`(allow mach-lookup (global-name "com.apple.cfprefsd.agent") (global-name "com.apple.cfprefsd.daemon") (global-name "com.apple.logd") (global-name "com.apple.logd.events") (global-name "com.apple.secinitd") (global-name "com.apple.system.DirectoryService.libinfo_v1") (global-name "com.apple.system.logger") (global-name "com.apple.system.notification_center") (global-name "com.apple.system.opendirectoryd.libinfo") (global-name "com.apple.system.opendirectoryd.membership") (global-name "com.apple.trustd") (global-name "com.apple.trustd.agent") (local-name "com.apple.cfprefsd.agent"))` + "\n")
}

func writeDarwinSystemFiles(b *strings.Builder) {
	for _, p := range darwinRuntimeReadPaths {
		b.WriteString(allowReadSubtreeLine(p))
	}
	for _, p := range darwinRuntimeReadLiterals {
		b.WriteString(allowReadLiteralLine(p))
	}
	b.WriteString(`(allow file-read* file-test-existence (subpath "/dev/fd"))` + "\n")
	b.WriteString(`(allow file-write-data file-ioctl (literal "/dev/null") (literal "/dev/zero") (subpath "/dev/fd"))` + "\n")
}

func writeDarwinConfiguredFiles(b *strings.Builder, cfg Config) {
	// Grant both the literal path AND its symlink-resolved target. A
	// caller listing a symlinked binary (e.g. /opt/homebrew/bin/mimo ->
	// a node script, or ~/.grok/bin/grok -> a downloads/ binary) must be
	// able to READ the symlink at its original location, or sandbox-exec
	// fails to exec through it ("execvp ... Operation not permitted").
	// Canonical-only grants dropped the symlink entry point.
	for _, raw := range cfg.ReadOnlyPaths {
		for _, p := range sandboxPathVariants(raw) {
			b.WriteString(allowReadSubtreeLine(p))
			b.WriteString(allowAncestorMetadataLine(p))
		}
	}
	for _, raw := range cfg.ReadWritePaths {
		for _, p := range sandboxPathVariants(raw) {
			b.WriteString(allowReadWriteSubtreeLine(p))
			b.WriteString(allowAncestorMetadataLine(p))
		}
	}
	if p := canonicalSandboxPath(cfg.WorkingDir); p != "" {
		b.WriteString(allowWorkingDirLine(p))
	}
	for _, p := range cfg.DenyWritePaths {
		if p = canonicalSandboxPath(p); p != "" {
			b.WriteString(allowReadSubtreeLine(p))
			b.WriteString(denyWriteSubtreeLine(p))
		}
	}
}

func writeDarwinCredentialDenies(b *strings.Builder, home string, extra []string) {
	// These regexes protect every local user even if home resolution fails.
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode file-read* file-write* (regex #"^/Users/[^/]+/\.ssh/"))` + "\n")
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode file-read* file-write* (regex #"^/Users/[^/]+/\.aws/"))` + "\n")
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode file-read* file-write* (regex #"^/Users/[^/]+/\.docker/config\.json$"))` + "\n")
	b.WriteString(`(deny file-read-data file-write-data file-write-create file-write-unlink file-write-mode file-read* file-write* (regex #"^/Users/[^/]+/\.docker/run/docker\.sock$"))` + "\n")
	for _, p := range MergeDenyPaths(home, extra) {
		// /var/run/docker.sock may be a per-user Docker Desktop symlink.
		// Keep the profile deterministic; the cross-user regex above covers
		// its target without embedding the build host's home path.
		if p == "/var/run/docker.sock" {
			b.WriteString(denySubtreeLine(p))
			continue
		}
		if p = canonicalSandboxPath(p); p != "" {
			b.WriteString(denySubtreeLine(p))
		}
	}
}

func writeDarwinNetworkPolicy(b *strings.Builder, policy string) {
	if policy == NetworkHost {
		b.WriteString(`(allow mach-lookup (global-name "com.apple.SystemConfiguration.SCNetworkReachability") (global-name "com.apple.dnssd.service"))` + "\n")
		b.WriteString("(allow network*)\n")
		return
	}
	// Zero, deny, proxy, and unknown values all fail closed. Proxy remains
	// closed until its UDS-only rules and daemon are implemented together.
	b.WriteString("(deny network*)\n")
}

func allowReadSubtreeLine(p string) string {
	return "(allow file-read* file-test-existence (subpath " + strconv.Quote(p) + "))\n"
}

func allowReadLiteralLine(p string) string {
	return "(allow file-read* file-test-existence (literal " + strconv.Quote(p) + "))\n"
}

func allowReadWriteSubtreeLine(p string) string {
	return "(allow file-read* file-test-existence file-write* (subpath " + strconv.Quote(p) + "))\n"
}

func allowWorkingDirLine(p string) string {
	q := strconv.Quote(p)
	return "(allow file-read-metadata file-test-existence (path-ancestors " + q + ") (literal " + q + "))\n"
}

// allowAncestorMetadataLine grants metadata (lstat) access to every
// ancestor of a granted path. Programs resolving a real path walk from
// the filesystem root and lstat each component — node's realpathSync
// lstats "/opt" en route to /opt/homebrew/... and aborts with "EPERM
// lstat '/opt'" when the ancestor is denied. This grants only metadata,
// not contents, so it does not widen read access to sibling files.
func allowAncestorMetadataLine(p string) string {
	return "(allow file-read-metadata file-test-existence (path-ancestors " + strconv.Quote(p) + "))\n"
}

func denySubtreeLine(p string) string {
	return "(deny file-read* file-write* file-read-data file-write-data file-write-create file-write-unlink file-write-mode (subpath " + strconv.Quote(p) + "))\n"
}

func denyWriteSubtreeLine(p string) string {
	p = strings.TrimRight(p, "/")
	pattern := "^" + regexEscapeForSBPL(p) + "(/|$)"
	return "(deny file-write-data file-write-create file-write-unlink file-write-mode file-write* (regex #\"" + pattern + "\"))\n"
}

// sandboxPathVariants returns the distinct read-grant paths for a caller
// path: its cleaned literal form plus its symlink-resolved target when
// they differ. Both are needed so a symlinked binary is readable at its
// symlink location (to exec through it) and at its real target. Returns
// nil for an empty input.
func sandboxPathVariants(p string) []string {
	if p == "" {
		return nil
	}
	clean := filepath.Clean(p)
	canonical := canonicalSandboxPath(p)
	if canonical == "" || canonical == clean {
		return []string{clean}
	}
	return []string{clean, canonical}
}

func canonicalSandboxPath(p string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

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
