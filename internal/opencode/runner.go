// runner.go isolates every os/exec call the opencode Manager makes
// behind a small commandRunner interface. Production wires up
// execCommandRunner (real subprocesses); tests inject fakes so unit
// tests never spawn the real binary.
package opencode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// commandRunner is the seam tests use to replace the real exec layer.
// In production execCommandRunner spawns real processes; tests inject
// fakes that capture invocations and return canned stdout/exit codes.
type commandRunner interface {
	// LookPath mimics exec.LookPath.
	LookPath(name string) (string, error)
	// ProbeKnownPaths checks well-known install locations for opencode
	// and returns the first existing executable path, or "" if none
	// match. Used as the fallback when launchd-stripped PATH causes
	// LookPath to fail.
	ProbeKnownPaths() string
	// Output runs a short-lived command to completion and returns stdout.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Start spawns a long-lived process and returns a handle the manager
	// can signal + wait on.
	Start(ctx context.Context, name string, args ...string) (processHandle, error)
}

// knownOpenCodePaths are install locations to probe when PATH lookup
// fails. Order matches expected user preference: per-user installs
// (their own opencode) win over Homebrew system installs. $HOME is
// resolved at probe time so a daemon restart picks up new homes.
func knownOpenCodePaths() []string {
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, ".opencode/bin/opencode"),
		filepath.Join(home, ".local/bin/opencode"),
		filepath.Join(home, "bin/opencode"),
		"/opt/homebrew/bin/opencode",
		"/usr/local/bin/opencode",
	}
	return paths
}

// processHandle is the minimal interface the supervisor needs from a
// running child. It exists so the runner seam can fake long-running
// processes without spawning real ones in unit tests.
type processHandle interface {
	Signal(sig syscall.Signal) error
	Kill() error
	Wait() error
}

// execCommandRunner is the production commandRunner that shells out to
// the real binary. Tests replace this with a fake to avoid spawning
// real processes.
type execCommandRunner struct{}

func (execCommandRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (execCommandRunner) ProbeKnownPaths() string {
	for _, p := range knownOpenCodePaths() {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return p
		}
	}
	return ""
}

func (execCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func (execCommandRunner) Start(ctx context.Context, name string, args ...string) (processHandle, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderrLogger{}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execHandle{cmd: cmd}, nil
}

// execHandle adapts *exec.Cmd to the processHandle interface.
type execHandle struct {
	cmd *exec.Cmd
}

func (h *execHandle) Signal(sig syscall.Signal) error {
	if h.cmd.Process == nil {
		return errors.New("process not started")
	}
	return h.cmd.Process.Signal(sig)
}

func (h *execHandle) Kill() error {
	if h.cmd.Process == nil {
		return errors.New("process not started")
	}
	return h.cmd.Process.Kill()
}

func (h *execHandle) Wait() error {
	return h.cmd.Wait()
}

// stderrLogger forwards each non-blank stderr line from opencode to
// slog at WARN. Matches the downstream-manager pattern so daemon logs
// stay grep-able by subsystem.
type stderrLogger struct {
	buf []byte
}

func (l *stderrLogger) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		idx := bytes.IndexByte(l.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(l.buf[:idx]))
		l.buf = l.buf[idx+1:]
		if line != "" {
			slog.Warn("opencode stderr", "line", line)
		}
	}
	return len(p), nil
}
