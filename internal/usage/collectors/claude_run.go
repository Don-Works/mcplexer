package collectors

import (
	"context"
	"fmt"
	"os/exec"
)

func runClaudeUsageProbe(ctx context.Context, binary string) ([]byte, error) {
	return runClaudeUsagePTY(ctx, binary)
}

func runClaudeAuthStatus(ctx context.Context, binary string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, "auth", "status", "--json") //nolint:gosec
	output, err := cmd.Output()
	if len(output) > 64<<10 {
		output = output[:64<<10]
	}
	if err != nil {
		return nil, fmt.Errorf("claude auth status failed")
	}
	return output, nil
}
