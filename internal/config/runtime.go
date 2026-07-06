package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeInfo is the effective runtime state a running daemon publishes so
// out-of-process tools (doctor, rules sync) can discover where it actually
// listens — the HTTP address comes from a --addr flag the daemon received,
// which loadConfig() (env + defaults only) never sees. It carries no secrets.
type RuntimeInfo struct {
	HTTPAddr   string `json:"http_addr"`
	PublicURL  string `json:"public_url,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	PID        int    `json:"pid"`
	Version    string `json:"version,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
}

// RuntimeInfoPath is where the descriptor lives inside the data dir.
func RuntimeInfoPath(dataDir string) string {
	return filepath.Join(dataDir, "runtime.json")
}

// WriteRuntimeInfo atomically publishes the descriptor (0600).
func WriteRuntimeInfo(dataDir string, info RuntimeInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime info: %w", err)
	}
	path := RuntimeInfoPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write runtime info: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename runtime info: %w", err)
	}
	return nil
}

// ReadRuntimeInfo loads the descriptor. Missing file returns (nil, nil) — a
// stale or absent file is a discovery miss, not an error; callers fall back.
func ReadRuntimeInfo(dataDir string) (*RuntimeInfo, error) {
	data, err := os.ReadFile(RuntimeInfoPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read runtime info: %w", err)
	}
	var info RuntimeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse runtime info: %w", err)
	}
	return &info, nil
}

// RemoveRuntimeInfo deletes the descriptor on clean shutdown (best-effort).
func RemoveRuntimeInfo(dataDir string) {
	_ = os.Remove(RuntimeInfoPath(dataDir))
}

// DashboardURL derives the browser URL for the local dashboard. A configured
// public URL (e.g. a reverse-proxy / tailscale HTTPS origin) wins; otherwise
// the URL is built from the effective listen port so it points at the port the
// daemon actually bound, not a compiled-in default. A bind on 0.0.0.0 or ::
// is reported as localhost — that is the address a human opens.
func DashboardURL(httpAddr, publicURL string) string {
	if u := strings.TrimRight(strings.TrimSpace(publicURL), "/"); u != "" {
		return u
	}
	const fallback = "http://localhost:3333"
	addr := strings.TrimSpace(httpAddr)
	if addr == "" {
		return fallback
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fallback
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}
