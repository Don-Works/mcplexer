//go:build !(darwin || linux || freebsd || openbsd || netbsd)

package collectors

import (
	"context"
	"errors"
)

func runGrokBillingPTY(ctx context.Context, binary string, debugPath string) ([]byte, error) {
	_ = ctx
	_ = binary
	_ = debugPath
	return nil, errors.New("grok billing probe requires a unix PTY")
}