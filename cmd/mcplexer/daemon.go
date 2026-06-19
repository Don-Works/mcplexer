package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/install"
)

const (
	pidFile = "mcplexer.pid"
	logFile = "mcplexer.log"
	dbFile  = "mcplexer.db"
)

// dataDir returns ~/.mcplexer, creating it if needed.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".mcplexer")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	return dir, nil
}

func cmdDaemon(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mcplexer daemon <start|stop|status|logs|uninstall>")
	}

	switch args[0] {
	case "start":
		return daemonStart(args[1:])
	case "stop":
		return daemonStop()
	case "status":
		return daemonStatus()
	case "logs":
		return daemonLogs(args[1:])
	case "uninstall":
		return daemonUninstall()
	default:
		return fmt.Errorf("unknown daemon command: %s\nUsage: mcplexer daemon <start|stop|status|logs|uninstall>", args[0])
	}
}

func daemonStart(args []string) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}

	// Check if already running
	if pid, ok := readPID(dir); ok {
		if processAlive(pid) {
			return fmt.Errorf("daemon already running (PID %d)", pid)
		}
		// Stale PID file
		_ = os.Remove(filepath.Join(dir, pidFile))
	}

	if launchdInstalled() {
		if err := launchdStart(); err != nil {
			return fmt.Errorf("launchctl start: %w", err)
		}
		fmt.Println("MCPlexer started via launchd")
		return nil
	}

	// Parse flags with defaults
	addr := os.Getenv("MCPLEXER_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:3333"
	}
	socketPath := install.DefaultSocketPath()
	for _, arg := range args {
		if strings.HasPrefix(arg, "--addr=") {
			addr = arg[7:]
		}
		if strings.HasPrefix(arg, "--socket=") {
			socketPath = arg[9:]
		}
	}

	// Open log file. Mode 0600: log can contain peer IDs, paths, and
	// imperfectly-redacted errors from downstream MCP servers — we don't
	// want a world-readable copy on multi-user boxes. ensureLogFile is
	// idempotent and tightens any legacy 0644 file from a pre-fix daemon.
	logPath := filepath.Join(dir, logFile)
	if err := ensureLogFile(logPath); err != nil {
		return fmt.Errorf("ensure log file: %w", err)
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer func() { _ = lf.Close() }()

	// Build serve command arguments
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	serveArgs := []string{
		"serve",
		"--mode=http",
		"--addr=" + addr,
		"--socket=" + socketPath,
	}

	cmd := exec.Command(exe, serveArgs...)
	cmd.Stderr = lf
	cmd.Stdout = lf
	// Route slog through lumberjack rotation in the child process. The
	// child's stdout/stderr are still piped to logPath for panic capture;
	// the dominant log volume (slog JSON lines) now rotates cleanly.
	cmd.Env = append(os.Environ(), "MCPLEXER_LOG_PATH="+logPath)
	// Detach from parent process group
	daemonSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	pid := cmd.Process.Pid

	// Write PID file
	pidPath := filepath.Join(dir, pidFile)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		// Kill the child if we can't write PID
		_ = cmd.Process.Kill()
		return fmt.Errorf("write pid file: %w", err)
	}

	// Release the child process so it survives our exit
	_ = cmd.Process.Release()

	fmt.Printf("MCPlexer daemon started on %s (PID %d)\n", addr, pid)
	fmt.Printf("  Logs: %s\n", logPath)
	fmt.Printf("  DB:   %s\n", filepath.Join(dir, dbFile))
	fmt.Printf("  UI:   %s\n", httpURLFromAddr(addr))
	return nil
}

func daemonStop() error {
	if launchdInstalled() {
		if err := launchdStop(); err != nil {
			return fmt.Errorf("launchctl stop: %w", err)
		}
		fmt.Println("MCPlexer stopped via launchd")
		return nil
	}

	dir, err := dataDir()
	if err != nil {
		return err
	}

	pid, ok := readPID(dir)
	if !ok {
		return fmt.Errorf("daemon not running (no PID file)")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(filepath.Join(dir, pidFile))
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := signalTerminate(proc); err != nil {
		_ = os.Remove(filepath.Join(dir, pidFile))
		return fmt.Errorf("send SIGTERM to PID %d: %w", pid, err)
	}

	// Wait for process to exit (up to 5 seconds)
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	_ = os.Remove(filepath.Join(dir, pidFile))
	fmt.Println("MCPlexer daemon stopped")
	return nil
}

func daemonStatus() error {
	if launchdInstalled() {
		running, _ := launchdStatus()
		if running {
			fmt.Println("MCPlexer daemon: running (launchd)")
		} else {
			fmt.Println("MCPlexer daemon: stopped (launchd installed)")
		}
		return nil
	}

	dir, err := dataDir()
	if err != nil {
		return err
	}

	pid, ok := readPID(dir)
	if ok && processAlive(pid) {
		fmt.Printf("MCPlexer daemon: running (PID %d)\n", pid)
		return nil
	}
	if ok && !processAlive(pid) {
		_ = os.Remove(filepath.Join(dir, pidFile))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	socketPath := install.DefaultSocketPath()
	if probeSocket(ctx, socketPath) {
		fmt.Printf("MCPlexer daemon: running (socket %s)\n", socketPath)
		return nil
	}

	addr := os.Getenv("MCPLEXER_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:3333"
	}
	url := httpURLFromAddr(addr) + "/healthz"
	if probeHealthEndpoint(ctx, url) {
		fmt.Printf("MCPlexer daemon: running (health %s)\n", url)
		return nil
	}

	fmt.Println("MCPlexer daemon: not running")
	return nil
}

func probeHealthEndpoint(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func daemonLogs(args []string) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}

	logPath := filepath.Join(dir, logFile)
	follow := false
	for _, arg := range args {
		if arg == "-f" || arg == "--follow" {
			follow = true
		}
	}

	tailArgs := []string{"-n", "100"}
	if follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, logPath)

	cmd := exec.Command("tail", tailArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func daemonUninstall() error {
	if !launchdInstalled() {
		fmt.Println("No launchd agent installed")
		return nil
	}
	if err := uninstallLaunchd(); err != nil {
		return fmt.Errorf("uninstall launchd: %w", err)
	}
	fmt.Println("MCPlexer launchd agent uninstalled")
	return nil
}

// readPID reads the PID from the PID file. Returns 0, false if not found.
func readPID(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, pidFile))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return pid, true
}

// processAlive checks if a process with the given PID is still running.
// Implemented in daemon_unix.go and daemon_windows.go.
