package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/don-works/mcplexer/internal/install"
)

// stdio cold-start fix: proxy through the daemon local IPC endpoint
//
// Every `mcplexer` invocation defaulting to stdio mode used to build its own
// downstream MCP manager, opening every configured remote (Linear, Fetch,
// Playwright, Vercel, ...). That cold-start cost showed up in the daemon log
// as "list tools slow" warnings 3–7s per remote per stdio subprocess, and got
// worse under sub-agent concurrency: six parallel agents = six independent
// tools/list calls per remote, which rate-limit and back off.
//
// Fix: when the daemon is up and reachable on its local IPC endpoint, the
// stdio subprocess proxies stdin/stdout through that socket via the existing
// `mcplexer connect` machinery. The daemon's downstream connections are
// already warm, so first-tools-list goes from multi-second cold-start to a
// single round-trip over a UDS.
//
// Fallback: if there's no daemon, or the socket fails the ping check, we fall
// back to the existing in-process stdio path so a standalone `mcplexer`
// invocation still works (matters for `mcplexer dry-run`, fresh-install
// before-setup, tests, and headless boxes without the daemon installed).

const (
	// daemonProbeTimeout caps the dial + ping round-trip before we give
	// up and fall back to in-process. Anything longer would defeat the
	// point: cold-start latency is the bug we're fixing.
	daemonProbeTimeout = 250 * time.Millisecond
)

// findDaemonSocket returns the path to a healthy daemon IPC endpoint, or
// "" if no daemon is reachable. The probe consists of dialling the socket
// and exchanging a minimal MCP `ping` round-trip — a pure connection check
// would pass even against a half-stuck listener.
//
// Override the probed path with MCPLEXER_SOCKET_PATH=/path/to/sock.
// Skip the probe entirely with MCPLEXER_NO_PROXY=1 (debug / fallback
// escape hatch).
func findDaemonSocket(ctx context.Context) string {
	if os.Getenv("MCPLEXER_NO_PROXY") == "1" {
		return ""
	}
	candidate := os.Getenv("MCPLEXER_SOCKET_PATH")
	if candidate == "" {
		candidate = install.DefaultSocketPath()
	}
	if !localIPCPathLikelyPresent(candidate) {
		return ""
	}
	if !probeSocket(ctx, candidate) {
		return ""
	}
	return candidate
}

// probeSocket dials the candidate socket, sends an MCP `ping` request, and
// waits for any well-formed JSON-RPC reply. Returns true only when the
// full round-trip completes within daemonProbeTimeout.
func probeSocket(ctx context.Context, path string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	defer cancel()

	conn, err := dialLocalIPCContext(probeCtx, path)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	deadline, ok := probeCtx.Deadline()
	if ok {
		_ = conn.SetDeadline(deadline)
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      "mcplexer-stdio-probe",
		"method":  "ping",
	}
	enc, err := json.Marshal(req)
	if err != nil {
		return false
	}
	enc = append(enc, '\n')
	if _, err := conn.Write(enc); err != nil {
		return false
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return false
	}
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return false
	}
	return resp.JSONRPC == "2.0"
}

// tryStdioProxy attempts to proxy stdin/stdout through the daemon socket.
// Returns (true, err) when a daemon socket was found and we did proxy —
// callers should propagate err verbatim and NOT fall back. Returns
// (false, nil) when no socket was available and the caller should run the
// in-process stdio path.
//
// This reuses the existing bridgeStdioToSocket machinery (cmd/mcplexer/
// connect.go), which already implements reconnect-with-backoff, init
// handshake replay, `initialized` notification capture/replay, roots
// injection, and clean shutdown on stdin EOF. Reusing it means the proxy
// path is exercised by every install that already uses `mcplexer connect`
// in its MCP-client config — no fresh code surface here.
func tryStdioProxy(ctx context.Context) (bool, error) {
	socket := findDaemonSocket(ctx)
	if socket == "" {
		return false, nil
	}
	slog.Info("stdio: proxying through daemon socket (warm downstream)",
		"socket", socket)
	err := bridgeStdioToSocket(ctx, os.Stdin, os.Stdout, socket)
	// bridgeStdioToSocket returns nil on clean stdin EOF or ctx
	// cancellation. A non-nil error reflects an unrecoverable dial / IO
	// failure — we surface it rather than masking with the in-process
	// fallback, because if the daemon was advertising a socket and then
	// died, the right answer is "tell the operator", not "silently spawn
	// 12 cold downstream stacks".
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("stdio proxy exited with error", "err", err)
	}
	return true, err
}
