package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdDoctor(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	checks := []healthCheck{
		{"binary", checkBinary()},
		{"port", checkPort(cfg.HTTPAddr)},
		{"database", checkDatabase(cfg)},
		{"socket", checkSocket(cfg.SocketPath)},
		{"daemon", checkDaemon(cfg.HTTPAddr)},
		{"age_key", checkAgeKey(cfg)},
		{"api_key", checkAPIKey(cfg)},
	}

	passed := 0
	for _, c := range checks {
		status := "✓"
		if !c.result.ok {
			status = "✗"
		} else {
			passed++
		}
		fmt.Printf("  %s %-12s %s\n", status, c.name, c.result.message)
		if c.result.remediation != "" && !c.result.ok {
			fmt.Printf("    → %s\n", c.result.remediation)
		}
	}

	fmt.Printf("\n%d/%d checks passed\n", passed, len(checks))

	if passed < len(checks) {
		return fmt.Errorf("some checks failed")
	}
	return nil
}

type healthCheck struct {
	name   string
	result checkResult
}

type checkResult struct {
	ok          bool
	message     string
	remediation string
}

// checkBinary verifies the mcplexer binary is reachable.
func checkBinary() checkResult {
	exe, err := os.Executable()
	if err != nil {
		return checkResult{false, "cannot resolve executable path", "ensure mcplexer is in PATH"}
	}
	if _, err := os.Stat(exe); err != nil {
		return checkResult{false, "binary not found: " + exe, "reinstall mcplexer"}
	}
	v := resolveMCPlexerVersion()
	return checkResult{true, "mcplexer " + v, ""}
}

// checkPort tests whether the configured HTTP port is available.
func checkPort(addr string) checkResult {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if isDaemonHealthy(addr) {
			return checkResult{true, fmt.Sprintf("port %s in use by running daemon", addr), ""}
		}
		return checkResult{false, fmt.Sprintf("port %s is in use", addr),
			fmt.Sprintf("stop the process using port %s or change MCPLEXER_HTTP_ADDR", addr)}
	}
	_ = ln.Close()
	return checkResult{true, fmt.Sprintf("port %s available", addr), ""}
}

// isDaemonHealthy checks if the daemon responds OK on the health endpoint.
func isDaemonHealthy(addr string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/api/v1/health", addr))
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// checkDatabase opens the SQLite database and runs a simple query.
func checkDatabase(cfg *Config) checkResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		if isDBLockedError(err) {
			return checkResult{false, "database is locked",
				"stop other mcplexer processes: mcplexer daemon stop"}
		}
		return checkResult{false, fmt.Sprintf("cannot open database: %v", err),
			fmt.Sprintf("check that %s is writable and disk space is available", filepath.Dir(cfg.DBDSN))}
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ListWorkspaces(ctx); err != nil {
		return checkResult{false, fmt.Sprintf("database query failed: %v", err),
			"database may be corrupted; try restoring from backup"}
	}
	return checkResult{true, fmt.Sprintf("accessible (%s)", cfg.DBDSN), ""}
}

// checkSocket verifies the socket path directory exists (if configured).
func checkSocket(socketPath string) checkResult {
	if socketPath == "" {
		return checkResult{true, "not configured (skipped)", ""}
	}
	dir := filepath.Dir(socketPath)
	info, err := os.Stat(dir)
	if err != nil {
		return checkResult{false, fmt.Sprintf("socket directory %s does not exist: %v", dir, err),
			"ensure the daemon has created the directory or create it manually"}
	}
	if !info.IsDir() {
		return checkResult{false, fmt.Sprintf("socket path %s is not a directory", dir),
			"check socket path configuration"}
	}
	return checkResult{true, fmt.Sprintf("exists (%s)", dir), ""}
}

// checkDaemon attempts to reach the running daemon's health endpoint.
func checkDaemon(addr string) checkResult {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s/api/v1/health", addr)
	resp, err := client.Get(url)
	if err != nil {
		return checkResult{false, "daemon not responding",
			"start the daemon: mcplexer daemon start"}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return checkResult{false, fmt.Sprintf("daemon returned HTTP %d", resp.StatusCode),
			"check daemon logs for errors"}
	}

	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return checkResult{true, "responding (unparseable body)", ""}
	}
	v := body.Version
	if v == "" {
		v = "unknown"
	}
	return checkResult{true, fmt.Sprintf("running (version %s)", v), ""}
}

// checkAgeKey verifies the age encryption key file exists.
func checkAgeKey(cfg *Config) checkResult {
	keyPath := cfg.AgeKeyPath
	if keyPath == "" {
		keyPath = defaultDataPath("age-key.txt")
	}
	if _, err := os.Stat(keyPath); err != nil {
		return checkResult{false, "age key not found at " + keyPath,
			"generate a key: mcplexer init"}
	}
	return checkResult{true, "exists (" + keyPath + ")", ""}
}

// checkAPIKey verifies the API key file exists.
func checkAPIKey(cfg *Config) checkResult {
	keyPath := cfg.APITokenPath
	if keyPath == "" {
		keyPath = defaultDataPath("api-key")
	}
	if _, err := os.Stat(keyPath); err != nil {
		return checkResult{false, "API key not found at " + keyPath,
			"generate a key: mcplexer init"}
	}
	return checkResult{true, "exists (" + keyPath + ")", ""}
}
