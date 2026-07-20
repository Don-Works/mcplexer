//go:build linux

package sandbox

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// bwrapDriver implements Driver using bubblewrap (`bwrap`). This is the
// preferred Linux path on Raspberry Pi appliances because bwrap is a
// small SUID-root helper packaged across Debian/Ubuntu/Alpine and gives
// us per-call user-namespace + bind-mount isolation without a daemon.
//
// NOTE: UNVERIFIED FROM macOS DEVELOPMENT MACHINE. This compiles under
// `GOOS=linux go build`, but the M2 author was unable to run a live
// sandbox on a Linux host. A follow-up integration test on the Pi
// appliance must validate this end-to-end before relying on it.
type bwrapDriver struct{}

func (d *bwrapDriver) Name() string { return "bwrap" }

func (d *bwrapDriver) Available() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("bwrap")
	return err == nil
}

// Run builds a bwrap argv from cfg and execs it. Stdio is wired
// through; ctx cancellation kills the inner process via
// exec.CommandContext.
//
// Argv shape:
//
//	bwrap --unshare-all [--share-net]
//	  --proc /proc --dev /dev --tmpfs /tmp
//	  --ro-bind <p> <p> ...   (for each ReadOnlyPath, minus deny)
//	  --bind   <p> <p> ...   (for each ReadWritePath, minus deny)
//	  --chdir <cwd> --die-with-parent
//	  -- <program> <args>...
func (d *bwrapDriver) Run(
	ctx context.Context, cfg Config, program string, args []string,
) (ExitCode, error) {
	if program == "" {
		return -1, errors.New("sandbox: program is required")
	}
	home, _ := os.UserHomeDir()
	argv := bwrapArgv(cfg, home, program, args)

	cmd := exec.CommandContext(ctx, "bwrap", argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = cleanSandboxEnv()

	err := cmd.Run()
	if cmd.ProcessState != nil {
		return ExitCode(cmd.ProcessState.ExitCode()), filterRunErr(err)
	}
	return -1, err
}

// linuxRuntimeReadPaths are immutable OS paths needed to start ordinary
// command-line programs, mirroring darwinRuntimeReadPaths. Third-party
// runtimes under $HOME (linuxbrew, nvm, ~/.local) intentionally are not
// implicit; callers list them in ReadOnlyPaths. /run/systemd/resolve is
// the systemd-resolved stub target of /etc/resolv.conf — without it DNS
// fails on most modern distros. Missing entries are skipped because
// bwrap refuses to bind a nonexistent source.
var linuxRuntimeReadPaths = []string{
	"/bin",
	"/sbin",
	"/usr",
	"/lib",
	"/lib32",
	"/lib64",
	"/libx32",
	"/etc",
	"/opt",
	"/run/systemd/resolve",
}

// bwrapArgv builds the full argv for the bwrap invocation. Split out
// so a unit test can exercise the assembly without launching bwrap.
func bwrapArgv(cfg Config, home, program string, args []string) []string {
	denied := MergeDenyPaths(home, cfg.DenyPaths)
	boundRoots := make([]string, 0, len(linuxRuntimeReadPaths)+len(cfg.ReadOnlyPaths)+len(cfg.ReadWritePaths))

	argv := []string{
		"--unshare-all",
		"--new-session",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--die-with-parent",
	}
	if cfg.Network == NetworkHost {
		argv = append(argv, "--share-net")
	}
	for _, p := range linuxRuntimeReadPaths {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		argv = append(argv, "--ro-bind", p, p)
		boundRoots = append(boundRoots, p)
	}
	for _, p := range cfg.ReadOnlyPaths {
		if p == "" || pathDeniedBy(p, denied) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			// Only home-relative grants are pinned to home: a config
			// path under $HOME that resolves elsewhere is a symlink
			// escape. System/runtime paths (/opt, /usr/local, a repo
			// outside home) legitimately live outside home and bind at
			// their resolved location.
			if isSubpath(p, home) && !isSubpath(resolved, home) {
				slog.Warn("sandbox: home path resolves outside home, skipping bind", "path", p, "resolved", resolved)
				continue
			}
			p = resolved
		}
		if pathDeniedBy(p, denied) {
			continue
		}
		argv = append(argv, "--ro-bind", p, p)
		boundRoots = append(boundRoots, p)
	}
	for _, p := range cfg.ReadWritePaths {
		if p == "" || pathDeniedBy(p, denied) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			if isSubpath(p, home) && !isSubpath(resolved, home) {
				slog.Warn("sandbox: home path resolves outside home, skipping bind", "path", p, "resolved", resolved)
				continue
			}
			p = resolved
		}
		if pathDeniedBy(p, denied) {
			continue
		}
		argv = append(argv, "--bind", p, p)
		boundRoots = append(boundRoots, p)
	}
	// DenyWritePaths: bind read-only so the subprocess can READ but not
	// WRITE. bwrap has no "deny just writes" primitive — `--ro-bind` is
	// the closest equivalent and gives subtree coverage by default.
	// Caveat: if the path doesn't already exist on the host, `--ro-bind`
	// errors out at start (bwrap refuses to bind a missing source) and
	// the spawn fails closed, which is acceptable for credential dirs
	// that always exist when claude_cli is in use.
	for _, p := range cfg.DenyWritePaths {
		if p == "" || pathDeniedBy(p, denied) {
			continue
		}
		argv = append(argv, "--ro-bind", p, p)
		boundRoots = append(boundRoots, p)
	}

	// A denied child remains visible when a broader parent directory is bind
	// mounted. Apply masks after every caller bind so mount ordering hides the
	// host subtree. Directories get an empty tmpfs; files get /dev/null. If an
	// intended mask cannot be mounted, bwrap itself fails the spawn closed.
	for _, p := range denied {
		if p == "" || !pathWithinAny(p, boundRoots) {
			continue
		}
		if info, err := os.Lstat(p); err == nil && !info.IsDir() {
			argv = append(argv, "--ro-bind", "/dev/null", p)
			continue
		}
		argv = append(argv, "--tmpfs", p)
	}
	// bwrap chdirs INSIDE the new namespace, whose root is a fresh tmpfs
	// containing only what we bound above. A chdir target that no bind
	// covers does not exist there and bwrap fails the spawn outright
	// ("Can't chdir to ...: No such file or directory"). The zero-value
	// Config hits this every time: WorkingDir is empty, so cwd falls back
	// to DefaultWorkingDir ("/workspace"), which nothing ever binds or
	// creates. Materialize an empty directory for any uncovered target so
	// the documented "zero-value Config is safe" contract actually holds.
	// This runs after the deny masks so no later mount can shadow it.
	cwd := cfg.WorkingDir
	if cwd == "" {
		cwd = DefaultWorkingDir
	}
	if !pathWithinAny(cwd, boundRoots) {
		argv = append(argv, "--dir", cwd)
	}
	argv = append(argv, "--chdir", cwd, "--", program)
	argv = append(argv, args...)
	return argv
}

func pathDeniedBy(path string, denied []string) bool {
	for _, deny := range denied {
		if deny != "" && isSubpath(path, deny) {
			return true
		}
	}
	return false
}

func pathWithinAny(path string, roots []string) bool {
	for _, root := range roots {
		if root != "" && isSubpath(path, root) {
			return true
		}
	}
	return false
}

func cleanSandboxEnv() []string {
	safe := map[string]struct{}{
		"PATH": {}, "HOME": {}, "USER": {}, "SHELL": {},
		"TERM": {}, "LANG": {}, "TMPDIR": {}, "TMP": {},
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

func isSubpath(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}
