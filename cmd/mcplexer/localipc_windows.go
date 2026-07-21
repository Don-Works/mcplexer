//go:build windows

package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func listenLocalIPCWithHandoff(
	ctx context.Context,
	path string,
	cfg listenHandoffConfig,
) (net.Listener, error) {
	path = normalizeWindowsPipePath(path)
	if !isWindowsPipePath(path) {
		return nil, fmt.Errorf("listen named pipe: invalid pipe path %q", path)
	}
	cfg = cfg.withDefaults()
	waitCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	delay := cfg.InitialDelay
	started := time.Now()
	logged := false
	var lastErr error
	for {
		ln, err := winio.ListenPipe(path, &winio.PipeConfig{
			SecurityDescriptor: "D:P(A;;GA;;;AU)",
			MessageMode:        false,
			InputBufferSize:    1024 * 1024,
			OutputBufferSize:   1024 * 1024,
		})
		if err == nil {
			return ln, nil
		}
		lastErr = err
		if !logged {
			logged = true
		}
		if err := waitListenRetry(waitCtx, delay); err != nil {
			return nil, fmt.Errorf("listen named pipe %s: %w; still unavailable after %s", path, lastErr, time.Since(started).Round(time.Millisecond))
		}
		delay = nextListenDelay(delay, cfg.MaxDelay)
	}
}

func dialLocalIPCContext(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, normalizeWindowsPipePath(path))
}

func localIPCPathLikelyPresent(path string) bool {
	return isWindowsPipePath(normalizeWindowsPipePath(path))
}

func localIPCDescription() string {
	return "named pipe"
}

func normalizeWindowsPipePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if isWindowsPipePath(path) {
		return path
	}
	path = strings.Trim(path, `\/`)
	return `\\.\pipe\` + path
}

func isWindowsPipePath(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(path, `/`, `\`))
	return strings.HasPrefix(p, `\\.\pipe\`)
}
