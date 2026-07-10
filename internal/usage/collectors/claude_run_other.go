//go:build !(darwin || linux || freebsd || openbsd || netbsd)

package collectors

import (
	"context"
	"errors"
)

func runClaudeUsagePTY(ctx context.Context, binary string) ([]byte, error) {
	_ = ctx
	_ = binary
	return nil, errors.New("claude usage probe requires a unix PTY")
}