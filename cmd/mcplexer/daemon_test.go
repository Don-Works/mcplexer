package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/install"
)

func TestProbeHealthEndpoint_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !probeHealthEndpoint(ctx, srv.URL+"/healthz") {
		t.Fatal("expected health probe to succeed for 200")
	}
}

func TestProbeHealthEndpoint_503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if probeHealthEndpoint(ctx, srv.URL+"/healthz") {
		t.Fatal("expected health probe to fail for 503")
	}
}

func TestProbeHealthEndpoint_Unreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if probeHealthEndpoint(ctx, "http://127.0.0.1:1/healthz") {
		t.Fatal("expected health probe to fail for unreachable")
	}
}

func TestProbeHealthEndpoint_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if probeHealthEndpoint(ctx, "http://127.0.0.1:1/healthz") {
		t.Fatal("expected health probe to fail for cancelled ctx")
	}
}

func TestDaemonStatus_ProbeSocketFallback(t *testing.T) {
	sockPath := shortSocketPath(t, "d")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go acceptPingOnce(ln)

	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	homeDir := setupFakeHome(t)

	if err := daemonStatus(); err != nil {
		t.Fatalf("daemonStatus: %v", err)
	}

	pidFile := filepath.Join(homeDir, "mcplexer.pid")
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("socket fallback should not create a PID file")
	}
}

func TestDaemonStatus_ProbeHealthFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sockPath := shortSocketPath(t, "x")
	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	t.Setenv("MCPLEXER_HTTP_ADDR", srv.Listener.Addr().String())
	setupFakeHome(t)

	if err := daemonStatus(); err != nil {
		t.Fatalf("daemonStatus: %v", err)
	}
}

func TestDaemonStatus_NotRunning(t *testing.T) {
	sockPath := shortSocketPath(t, "n")
	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	t.Setenv("MCPLEXER_HTTP_ADDR", "127.0.0.1:1")
	setupFakeHome(t)

	if err := daemonStatus(); err != nil {
		t.Fatalf("daemonStatus: %v", err)
	}
}

func TestDaemonStatus_PIDFileTakesPrecedence(t *testing.T) {
	sockPath := shortSocketPath(t, "p")
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
		_ = conn.Close()
	}()

	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	homeDir := setupFakeHome(t)

	pidPath := filepath.Join(homeDir, "mcplexer.pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := daemonStatus(); err != nil {
		t.Fatalf("daemonStatus: %v", err)
	}
}

func TestDaemonStatus_StalePIDFallsBackToSocket(t *testing.T) {
	sockPath := shortSocketPath(t, "s")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go acceptPingOnce(ln)

	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)
	homeDir := setupFakeHome(t)

	pidPath := filepath.Join(homeDir, "mcplexer.pid")
	if err := os.WriteFile(pidPath, []byte("999999998"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := daemonStatus(); err != nil {
		t.Fatalf("daemonStatus: %v", err)
	}
}

func TestDefaultSocketPath_EnvOverride(t *testing.T) {
	custom := "/custom/path/mcplexer.sock"
	t.Setenv("MCPLEXER_SOCKET_PATH", custom)

	got := install.DefaultSocketPath()
	if got != custom {
		t.Fatalf("DefaultSocketPath() = %q, want %q", got, custom)
	}
}

func TestProbeSocket_NonDefaultPath(t *testing.T) {
	sockPath := shortSocketPath(t, "c")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go acceptPingOnce(ln)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if !probeSocket(ctx, sockPath) {
		t.Fatal("probeSocket should succeed on a healthy non-default socket")
	}
}

func TestFindDaemonSocket_NonDefaultPath(t *testing.T) {
	sockPath := shortSocketPath(t, "f")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go acceptPingOnce(ln)

	t.Setenv("MCPLEXER_NO_PROXY", "")
	t.Setenv("MCPLEXER_SOCKET_PATH", sockPath)

	got := findDaemonSocket(context.Background())
	if got != sockPath {
		t.Fatalf("findDaemonSocket returned %q, want %q", got, sockPath)
	}
}

func TestListenTCPWithHandoffWaitsForRelease(t *testing.T) {
	oldLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen old tcp: %v", err)
	}
	addr := oldLn.Addr().String()

	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = oldLn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ln, err := listenTCPWithHandoff(ctx, addr, testListenHandoffConfig())
	if err != nil {
		t.Fatalf("listenTCPWithHandoff: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if got := ln.Addr().String(); got != addr {
		t.Fatalf("listener addr = %q, want %q", got, addr)
	}
}

func TestListenUnixWithHandoffRemovesStaleSocketFile(t *testing.T) {
	sockPath := shortSocketPath(t, "stale")
	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := listenUnixWithHandoff(context.Background(), sockPath, testListenHandoffConfig())
	if err != nil {
		t.Fatalf("listenUnixWithHandoff: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("expected %s to be a socket, mode=%v", sockPath, info.Mode())
	}
}

func TestListenUnixWithHandoffDoesNotRemoveLiveSocket(t *testing.T) {
	sockPath := shortSocketPath(t, "live")
	oldLn, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen old unix: %v", err)
	}
	defer func() { _ = oldLn.Close() }()

	go acceptAndCloseUntilClosed(oldLn)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	ln, err := listenUnixWithHandoff(ctx, sockPath, testListenHandoffConfig())
	if err == nil {
		_ = ln.Close()
		t.Fatal("expected listenUnixWithHandoff to fail while live socket is still owned")
	}

	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("live socket was removed: %v", err)
	}
	live, err := unixSocketHasListener(context.Background(), sockPath, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("probe live socket: %v", err)
	}
	if !live {
		t.Fatal("old listener should still own the socket")
	}
}

func TestStartingHTTPHandlerReportsStarting(t *testing.T) {
	srv := httptest.NewServer(startingHTTPHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/health") //nolint:gosec // test-local server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "starting" {
		t.Fatalf("status body = %q, want starting", body.Status)
	}
}

func setupFakeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	homeDir := filepath.Join(dir, ".mcplexer")
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	return homeDir
}

func shortSocketPath(t *testing.T, suffix string) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "mcpx-t-"+suffix)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func acceptPingOnce(ln net.Listener) {
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
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  map[string]any{},
	}
	enc, _ := json.Marshal(resp)
	enc = append(enc, '\n')
	_, _ = conn.Write(enc)
}

func acceptAndCloseUntilClosed(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func testListenHandoffConfig() listenHandoffConfig {
	return listenHandoffConfig{
		Timeout:      250 * time.Millisecond,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     20 * time.Millisecond,
		ProbeTimeout: 20 * time.Millisecond,
	}
}
