package clistats

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout   = 15 * time.Second
	defaultMaxOutput = 2 << 20
	maximumTimeout   = time.Minute
	maximumOutput    = 8 << 20
	defaultDays      = 30
	maxDays          = 3660
)

// RunStatus distinguishes a successful probe from a missing or failed CLI.
type RunStatus string

const (
	RunStatusOK          RunStatus = "ok"
	RunStatusUnavailable RunStatus = "unavailable"
	RunStatusError       RunStatus = "error"
)

var (
	ErrUnavailable    = errors.New("stats CLI unavailable")
	ErrCommandFailed  = errors.New("stats CLI failed")
	ErrOutputTooLarge = errors.New("stats CLI output exceeds limit")
)

// RunResult contains parsed model usage and a machine-readable status.
type RunResult struct {
	Status RunStatus
	Models []ModelStats
	Err    error
}

// CommandRunner is injectable so collectors can test without executing a CLI.
type CommandRunner interface {
	LookPath(file string) (string, error)
	CombinedOutput(ctx context.Context, name string, args []string, maxBytes int) ([]byte, error)
}

// Runner executes one supported CLI stats command without invoking a shell.
type Runner struct {
	Commands       CommandRunner
	Timeout        time.Duration
	MaxOutputBytes int
}

// NewRunner returns a runner with production-safe defaults.
func NewRunner(commands CommandRunner) *Runner {
	if commands == nil {
		commands = execRunner{}
	}
	return &Runner{Commands: commands, Timeout: defaultTimeout, MaxOutputBytes: defaultMaxOutput}
}

// Run executes <binary> stats --days N --models and parses its combined output.
func (r *Runner) Run(ctx context.Context, binary string, days int) RunResult {
	commands := r.Commands
	if commands == nil {
		commands = execRunner{}
	}
	resolved, err := commands.LookPath(strings.TrimSpace(binary))
	if err != nil || strings.TrimSpace(binary) == "" {
		return RunResult{Status: RunStatusUnavailable, Err: fmt.Errorf("%w: %q", ErrUnavailable, binary)}
	}
	days = normalizedDays(days)
	timeout, maxOutput := r.limits()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"stats", "--days", strconv.Itoa(days), "--models"}
	output, err := commands.CombinedOutput(runCtx, resolved, args, maxOutput)
	if err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return RunResult{Status: RunStatusError, Err: err}
		}
		return RunResult{Status: RunStatusError, Err: fmt.Errorf("%w: %v", ErrCommandFailed, err)}
	}
	models := ParseModelStatsTable(strings.Split(string(output), "\n"))
	return RunResult{Status: RunStatusOK, Models: models}
}

func normalizedDays(days int) int {
	if days <= 0 {
		return defaultDays
	}
	if days > maxDays {
		return maxDays
	}
	return days
}

func (r *Runner) limits() (time.Duration, int) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	} else if timeout > maximumTimeout {
		timeout = maximumTimeout
	}
	maxOutput := r.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutput
	} else if maxOutput > maximumOutput {
		maxOutput = maximumOutput
	}
	return timeout, maxOutput
}

type execRunner struct{}

func (execRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (execRunner) CombinedOutput(
	ctx context.Context, name string, args []string, maxBytes int,
) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output := &boundedBuffer{limit: maxBytes}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if output.overflow {
		return output.Bytes(), ErrOutputTooLarge
	}
	return output.Bytes(), err
}

type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if remaining < len(p) {
		b.overflow = true
	}
	return written, nil
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}
