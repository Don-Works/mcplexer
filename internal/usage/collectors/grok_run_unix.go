//go:build darwin || linux || freebsd || openbsd || netbsd

package collectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty/v2"
)

const grokPollInterval = 100 * time.Millisecond

func runGrokBillingPTY(ctx context.Context, binary string, debugPath string) ([]byte, error) {
	workDir, err := os.MkdirTemp("", "mcplexer-grok-usage-*")
	if err != nil {
		return nil, fmt.Errorf("grok temp workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	cmd := exec.CommandContext(
		ctx, binary, "--no-alt-screen", "--always-approve", "--no-memory",
		"--debug", "--debug-file", debugPath,
	)
	cmd.Dir = workDir
	cmd.Cancel = func() error { return grokKillProcessGroup(cmd) }
	cmd.WaitDelay = 3 * time.Second
	ptty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		return nil, fmt.Errorf("grok pty start: %w", err)
	}
	defer func() { _ = ptty.Close() }()
	go grokDrainPTY(ptty)
	found, readErr := grokWaitForBilling(ctx, debugPath)
	if readErr != nil {
		grokTerminate(cmd)
		_ = cmd.Wait()
		output, fileErr := readGrokDebugFile(debugPath, grokOutputCap)
		if fileErr != nil {
			return output, fileErr
		}
		return output, readErr
	}
	if found {
		grokTerminate(cmd)
	}
	waitErr := cmd.Wait()
	output, fileErr := readGrokDebugFile(debugPath, grokOutputCap)
	if fileErr != nil {
		return output, fileErr
	}
	if ctx.Err() != nil {
		return output, fmt.Errorf("grok billing probe timed out: %w", ctx.Err())
	}
	if !found {
		return output, errors.New("grok billing event not observed")
	}
	if waitErr != nil && !found && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return output, fmt.Errorf("grok billing probe: %w", waitErr)
	}
	return output, nil
}

func grokWaitForBilling(ctx context.Context, debugPath string) (bool, error) {
	ticker := time.NewTicker(grokPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			output, err := readGrokDebugFile(debugPath, grokOutputCap)
			if err != nil {
				return false, err
			}
			if parsed := parseGrokDebugOutput(output); len(parsed.windows) > 0 || parsed.plan != "" {
				return true, nil
			}
		}
	}
}

func grokDrainPTY(reader io.Reader) {
	buf := make([]byte, 4096)
	for {
		if _, err := reader.Read(buf); err != nil {
			return
		}
	}
}

func grokTerminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = grokKillProcessGroup(cmd)
}

func grokKillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
