package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFindDaemonSocket_Disabled — MCPLEXER_NO_PROXY=1 short-circuits
// regardless of whether a socket is up. Used as an escape hatch when
// debugging the in-process path.
func TestFindDaemonSocket_Disabled(t *testing.T) {
	t.Setenv("MCPLEXER_NO_PROXY", "1")
	if got := findDaemonSocket(context.Background()); got != "" {
		t.Fatalf("findDaemonSocket with MCPLEXER_NO_PROXY=1 returned %q, want \"\"", got)
	}
}

// TestFindDaemonSocket_Missing — when the candidate path does not exist
// on disk, the probe declines without dial attempts.
func TestFindDaemonSocket_Missing(t *testing.T) {
	t.Setenv("MCPLEXER_NO_PROXY", "")
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such.sock")
	t.Setenv("MCPLEXER_SOCKET_PATH", missing)
	if got := findDaemonSocket(context.Background()); got != "" {
		t.Fatalf("findDaemonSocket with missing socket returned %q, want \"\"", got)
	}
}

// TestProbeSocket_RespondsToPing — a fake daemon answering an MCP ping
// over a Unix socket is accepted by the probe. This is the happy path
// the cold-start fix relies on.
func TestProbeSocket_RespondsToPing(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		// Mimic the gateway's actual ping reply shape.
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{},
		}
		enc, _ := json.Marshal(resp)
		enc = append(enc, '\n')
		_, _ = conn.Write(enc)
	}()

	t.Setenv("MCPLEXER_NO_PROXY", "")
	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	if got := findDaemonSocket(context.Background()); got != sockPath {
		t.Fatalf("findDaemonSocket returned %q, want %q", got, sockPath)
	}
}

// TestProbeSocket_DeadServer — a Unix socket file present on disk but
// with nothing listening must fail the probe so the caller falls back to
// in-process. This guards against a stale socket left behind after a
// daemon crash.
func TestProbeSocket_DeadServer(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "stale.sock")
	// Create a regular file that won't accept connections; matches the
	// shape of a leftover socket node post-crash.
	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	t.Setenv("MCPLEXER_NO_PROXY", "")
	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	if got := findDaemonSocket(context.Background()); got != "" {
		t.Fatalf("findDaemonSocket on stale socket returned %q, want \"\"", got)
	}
}

// TestStdioProxy_EndToEnd — full pipeline: stdin → bridgeStdioToSocket →
// fake daemon → reply → stdout. Verifies that the proxy round-trips a
// real MCP initialize + tools/list inside a tight latency budget. This is
// the load-bearing acceptance test: a stdio subprocess proxying through
// the warm daemon must serve first-tools-list in <500ms (vs the 3-7s
// per-remote cold start the fix is correcting).
func TestStdioProxy_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Fake daemon: accept one connection, answer initialize + tools/list
	// per the gateway's actual JSON-RPC shape.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				JSONRPC string `json:"jsonrpc"`
				ID      any    `json:"id"`
				Method  string `json:"method"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				return
			}
			var resp map[string]any
			switch req.Method {
			case "initialize":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"protocolVersion": "2025-03-26",
						"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
						"serverInfo":      map[string]any{"name": "fake-daemon", "version": "0.0.0"},
					},
				}
			case "tools/list":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"tools": []map[string]any{
							{"name": "mcpx__search_tools", "description": "search"},
						},
					},
				}
			default:
				continue // ignore notifications etc.
			}
			enc, _ := json.Marshal(resp)
			enc = append(enc, '\n')
			if _, err := conn.Write(enc); err != nil {
				return
			}
		}
	}()

	// Drive the proxy with an in-memory client. We send initialize, then
	// tools/list, then close stdin (EOF) to terminate the proxy. The
	// proxy writes the daemon's replies to outBuf, which we then parse
	// to confirm both round-trips landed.
	initLine := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stdio-proxy-test","version":"0.0.0"}}}` + "\n"
	listLine := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"

	// Use io.Pipe rather than strings.NewReader so we can keep stdin
	// open between writes — closing stdin too early would race the
	// proxy and short-circuit the second request.
	stdinR, stdinW := io.Pipe()
	var outMu sync.Mutex
	outBuf := &lockedBuffer{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- bridgeStdioToSocket(ctx, stdinR, outBuf, sockPath)
	}()

	start := time.Now()

	// initialize
	if _, err := stdinW.Write([]byte(initLine)); err != nil {
		t.Fatalf("write init: %v", err)
	}
	if err := waitForFrame(ctx, outBuf, &outMu, `"id":1`); err != nil {
		t.Fatalf("waiting for initialize response: %v", err)
	}
	initLatency := time.Since(start)
	t.Logf("initialize round-trip: %v", initLatency)

	// tools/list
	listStart := time.Now()
	if _, err := stdinW.Write([]byte(listLine)); err != nil {
		t.Fatalf("write tools/list: %v", err)
	}
	if err := waitForFrame(ctx, outBuf, &outMu, `"id":2`); err != nil {
		t.Fatalf("waiting for tools/list response: %v", err)
	}
	listLatency := time.Since(listStart)
	t.Logf("tools/list round-trip: %v", listLatency)

	if listLatency > 500*time.Millisecond {
		t.Errorf("tools/list latency %v exceeded 500ms budget", listLatency)
	}

	// Clean shutdown: close stdin so bridgeStdioToSocket returns nil.
	_ = stdinW.Close()
	select {
	case err := <-proxyDone:
		if err != nil {
			t.Fatalf("proxy returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("proxy did not return after stdin close")
	}

	if !strings.Contains(outBuf.String(), "mcpx__search_tools") {
		t.Fatalf("proxy output missing tools/list payload: %q", outBuf.String())
	}
}

// lockedBuffer is the minimal io.Writer with thread-safe reads we use to
// capture proxy output in the E2E test.
type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func waitForFrame(ctx context.Context, buf *lockedBuffer, _ *sync.Mutex, needle string) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Second)
	}
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), needle) {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

// TestProbeSocket_SilentServer — a listening socket that never replies
// must time out and return false within the probe deadline.
func TestProbeSocket_SilentServer(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "silent.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept but never reply.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Hold the connection open without writing. probeSocket should
		// time out via SetDeadline.
		<-time.After(2 * time.Second)
		_ = conn.Close()
	}()

	t.Setenv("MCPLEXER_NO_PROXY", "")
	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	start := time.Now()
	got := findDaemonSocket(context.Background())
	elapsed := time.Since(start)
	if got != "" {
		t.Fatalf("findDaemonSocket on silent server returned %q, want \"\"", got)
	}
	// Probe is capped at daemonProbeTimeout (250ms). Allow a generous
	// upper bound to keep the test stable under load.
	if elapsed > 1*time.Second {
		t.Fatalf("findDaemonSocket on silent server took %v, expected fast timeout", elapsed)
	}
}
