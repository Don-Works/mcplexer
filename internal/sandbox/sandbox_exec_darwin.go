//go:build darwin

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
// a generated TinyScheme .sb profile. The profile is deny-by-default;
// callers must explicitly list non-system filesystem and network access.
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
