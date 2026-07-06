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

// bwrapArgv builds the full argv for the bwrap invocation. Split out
// so a unit test can exercise the assembly without launching bwrap.
func bwrapArgv(cfg Config, home, program string, args []string) []string {
	denied := denySet(MergeDenyPaths(home, cfg.DenyPaths))

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
	for _, p := range cfg.ReadOnlyPaths {
		if _, blocked := denied[p]; blocked || p == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			if !isSubpath(resolved, home) {
				slog.Warn("sandbox: symlink resolves outside home, skipping bind", "path", p, "resolved", resolved)
				continue
			}
			p = resolved
		}
		argv = append(argv, "--ro-bind", p, p)
	}
	for _, p := range cfg.ReadWritePaths {
		if _, blocked := denied[p]; blocked || p == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			if !isSubpath(resolved, home) {
				slog.Warn("sandbox: symlink resolves outside home, skipping bind", "path", p, "resolved", resolved)
				continue
			}
			p = resolved
		}
		argv = append(argv, "--bind", p, p)
	}
	// DenyWritePaths: bind read-only so the subprocess can READ but not
	// WRITE. bwrap has no "deny just writes" primitive — `--ro-bind` is
	// the closest equivalent and gives subtree coverage by default.
	// Caveat: if the path doesn't already exist on the host, `--ro-bind`
	// errors out at start (bwrap refuses to bind a missing source) and
	// the spawn fails closed, which is acceptable for credential dirs
	// that always exist when claude_cli is in use.
	for _, p := range cfg.DenyWritePaths {
		if _, blocked := denied[p]; blocked || p == "" {
			continue
		}
		argv = append(argv, "--ro-bind", p, p)
	}
	cwd := cfg.WorkingDir
	if cwd == "" {
		cwd = DefaultWorkingDir
	}
	argv = append(argv, "--chdir", cwd, "--", program)
	argv = append(argv, args...)
	return argv
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

// denySet turns the deny-path slice into a set for O(1) membership
// checks while filtering bind-mount entries. Exported via the package
// so unshare_linux.go can use the same helper.
func denySet(paths []string) map[string]struct{} {
	out := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		out[p] = struct{}{}
	}
	return out
}
