//go:build !windows

package main

import (
	"context"
	"net"
	"os"
)

func listenLocalIPCWithHandoff(
	ctx context.Context,
	path string,
	cfg listenHandoffConfig,
) (net.Listener, error) {
	return listenUnixWithHandoff(ctx, path, cfg)
}

func dialLocalIPCContext(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

func localIPCPathLikelyPresent(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func localIPCDescription() string {
	return "unix socket"
}
