//go:build darwin || linux || freebsd || openbsd || netbsd

package collectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty/v2"
	"github.com/don-works/mcplexer/internal/store"
)

func runClaudeUsagePTY(ctx context.Context, binary string) ([]byte, error) {
	workDir, err := claudeProbeWorkDir()
	if err != nil {
		return nil, fmt.Errorf("claude temp workspace: %w", err)
	}
	cmd := exec.CommandContext(ctx, binary, claudeArgv...)
	cmd.Dir = workDir
	cmd.WaitDelay = 3 * time.Second
	ptty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
	if err != nil {
		return nil, fmt.Errorf("claude pty start: %w", err)
	}
	defer func() { _ = ptty.Close() }()
	output := newCappedBuffer(claudeOutputCap)
	go claudeDrainPTY(ptty, output)
	if err := claudeSendUsage(ctx, ptty, output, workDir); err != nil {
		claudeTerminate(cmd)
		return output.Bytes(), err
	}
	found, readErr := claudeWaitForWindows(ctx, output)
	claudeTerminate(cmd)
	waitErr := cmd.Wait()
	out := output.Bytes()
	if readErr != nil {
		return out, readErr
	}
	if ctx.Err() != nil {
		return out, fmt.Errorf("claude usage probe timed out: %w", ctx.Err())
	}
	if !found {
		return out, errors.New("claude usage windows not observed")
	}
	if waitErr != nil && !found && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return out, fmt.Errorf("claude usage probe: %w", waitErr)
	}
	return out, nil
}

func claudeProbeWorkDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(cacheDir, "mcplexer", "claude-usage")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("probe workspace is not a regular directory")
	}
	return path, os.Chmod(path, 0o700)
}

func claudeSendUsage(
	ctx context.Context,
	ptty io.Writer,
	output *cappedBuffer,
	workDir string,
) error {
	ticker := time.NewTicker(claudePollInterval)
	defer ticker.Stop()
	deadline := time.Now().Add(claudeHandshakeLimit)
	readyAt := time.Now().Add(claudeStartupDelay)
	trustAnswered := false
	mcpAnswered := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			rendered := string(output.Bytes())
			if !trustAnswered && claudeTrustPrompt(rendered, workDir) {
				if _, err := ptty.Write([]byte("y\r")); err != nil {
					return err
				}
				trustAnswered = true
				readyAt = time.Now().Add(claudeStartupDelay)
				continue
			}
			if !mcpAnswered && claudeMCPPrompt(rendered) {
				if _, err := ptty.Write([]byte("3\r")); err != nil {
					return err
				}
				mcpAnswered = true
				readyAt = time.Now().Add(claudeStartupDelay)
				continue
			}
			if time.Now().After(readyAt) || time.Now().After(deadline) {
				_, err := ptty.Write([]byte(claudeUsageCommand))
				return err
			}
		}
	}
}

func claudeMCPPrompt(rendered string) bool {
	return strings.Contains(rendered, "New MCP server found") &&
		strings.Contains(rendered, "Enter selection [1-3]")
}

func claudeTrustPrompt(rendered, workDir string) bool {
	return strings.Contains(rendered, "Quick safety check") &&
		strings.Contains(rendered, filepath.Base(workDir)) &&
		strings.Contains(rendered, "Enter y/n")
}

func claudeWaitForWindows(ctx context.Context, output *cappedBuffer) (bool, error) {
	ticker := time.NewTicker(claudePollInterval)
	defer ticker.Stop()
	var readyAt time.Time
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
			parsed := parseClaudeUsageOutput(output.Bytes(), time.Now())
			if !claudeHasCoreWindows(parsed.windows) {
				readyAt = time.Time{}
				continue
			}
			if readyAt.IsZero() {
				readyAt = time.Now().Add(claudeSettleDelay)
			}
			if time.Now().After(readyAt) {
				return true, nil
			}
		}
	}
}

func claudeHasCoreWindows(windows []store.UsageWindow) bool {
	var session, week bool
	for _, window := range windows {
		label := strings.ToLower(window.Label)
		session = session || strings.HasPrefix(label, "current session")
		week = week || strings.HasPrefix(label, "current week (all models)")
	}
	return session && week
}

func claudeDrainPTY(reader io.Reader, output *cappedBuffer) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			_, _ = output.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func claudeTerminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = claudeKillProcessGroup(cmd)
}

func claudeKillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
