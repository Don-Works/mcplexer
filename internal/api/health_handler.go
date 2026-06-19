package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/readiness"
)

var startTime = time.Now()

// SystemInfo describes paths and modes the daemon is running with. The UI
// reads this to render "what's running where" without the user having to grep
// log files. Populated once at startup; nothing here is user-configurable
// post-boot.
type SystemInfo struct {
	Mode       string `json:"mode"` // "http" / "stdio" / "http+socket"
	Version    string `json:"version"`
	HTTPAddr   string `json:"http_addr,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	DataDir    string `json:"data_dir,omitempty"`
	ConfigFile string `json:"config_file,omitempty"`
	LogPath    string `json:"log_path,omitempty"`
	AddonsDir  string `json:"addons_dir,omitempty"`
	P2PEnabled bool   `json:"p2p_enabled"`
	// ServerProfile is "full" for a local workstation, or a focused server
	// profile such as "skills", "tasks", or "skills+tasks".
	ServerProfile string `json:"server_profile,omitempty"`
	// Capabilities lets the UI hide workstation-only surfaces when a daemon
	// is running as a shared server. Missing means "legacy/full".
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}

var systemInfo SystemInfo

var readinessTracker *readiness.Tracker

func SetSystemInfo(info SystemInfo) { systemInfo = info }

func SetReadinessTracker(t *readiness.Tracker) { readinessTracker = t }

type healthResponse struct {
	Status        string     `json:"status"`
	Version       string     `json:"version"`
	UptimeSeconds int        `json:"uptime_seconds"`
	Mode          string     `json:"mode"`
	System        SystemInfo `json:"system"`
}

func healthCheck(w http.ResponseWriter, _ *http.Request) {
	mode := systemInfo.Mode
	if mode == "" {
		mode = "http"
	}
	version := systemInfo.Version
	if version == "" {
		version = "0.1.0"
	}

	status := "ok"
	httpStatus := http.StatusOK
	if readinessTracker != nil {
		s := readinessTracker.State()
		switch s {
		case readiness.Ready:
			status = "ready"
		case readiness.Starting:
			status = "starting"
			httpStatus = http.StatusServiceUnavailable
		case readiness.Draining:
			status = "draining"
			httpStatus = http.StatusServiceUnavailable
		}
	}

	resp := healthResponse{
		Status:        status,
		Version:       version,
		UptimeSeconds: int(time.Since(startTime).Seconds()),
		Mode:          mode,
		System:        systemInfo,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(resp)
}

// systemReveal opens a known data path in the OS file manager (Finder on
// macOS, Explorer on Windows, xdg-open on Linux). Restricted to the daemon's
// own SystemInfo paths so the endpoint can't be turned into an arbitrary
// shell-out vector by a malicious page.
func systemReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	allowed := map[string]string{
		"data_dir":    systemInfo.DataDir,
		"config_file": systemInfo.ConfigFile,
		"log_path":    systemInfo.LogPath,
		"addons_dir":  systemInfo.AddonsDir,
	}
	path, ok := allowed[body.Target]
	if !ok {
		http.Error(w, "unknown target", http.StatusBadRequest)
		return
	}
	if path == "" {
		http.Error(w, "target not configured", http.StatusNotFound)
		return
	}

	// Resolve to absolute, then verify it stays inside the data dir (or one of
	// the explicitly-allowed siblings) before shelling out.
	abs, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, "resolve path", http.StatusInternalServerError)
		return
	}
	if !pathAllowed(abs, systemInfo) {
		http.Error(w, "path outside allowed roots", http.StatusForbidden)
		return
	}

	if err := openInFileManager(abs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathAllowed(abs string, info SystemInfo) bool {
	roots := []string{info.DataDir, info.AddonsDir}
	for _, root := range roots {
		if root == "" {
			continue
		}
		ar, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == ar || strings.HasPrefix(abs, ar+string(filepath.Separator)) {
			return true
		}
	}
	// Allow exact match for sibling files (config_file, log_path) even when
	// the parent isn't the data dir.
	for _, p := range []string{info.ConfigFile, info.LogPath} {
		if p == "" {
			continue
		}
		ap, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if abs == ap {
			return true
		}
	}
	return false
}

func openInFileManager(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// systemLaunchTerminal opens a terminal window with the working directory set
// to one of the daemon's known data paths (data_dir, addons_dir). The expected
// flow is: user clicks the button, terminal opens at ~/.mcplexer, user runs
// claude / opencode / codex there with mcplexer already wired up as an MCP
// server, and configures everything via mcpx__* tools.
//
// Allowed targets are restricted to directories — opening a terminal "at" the
// config_file or log_path doesn't make sense. The same allowlist + path-confine
// guardrails as systemReveal apply, so this endpoint can't be turned into an
// arbitrary shell-out.
func systemLaunchTerminal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Target == "" {
		body.Target = "data_dir"
	}

	allowed := map[string]string{
		"data_dir":   systemInfo.DataDir,
		"addons_dir": systemInfo.AddonsDir,
	}
	path, ok := allowed[body.Target]
	if !ok {
		http.Error(w, "unknown target (must be data_dir or addons_dir)", http.StatusBadRequest)
		return
	}
	if path == "" {
		http.Error(w, "target not configured", http.StatusNotFound)
		return
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, "resolve path", http.StatusInternalServerError)
		return
	}
	if !pathAllowed(abs, systemInfo) {
		http.Error(w, "path outside allowed roots", http.StatusForbidden)
		return
	}

	if err := openTerminalAt(abs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// openTerminalAt launches the user's terminal application with its working
// directory set to dir. The shell receives no command — just `cd dir`.
//
// macOS: prefer the user's current TERM_PROGRAM (iTerm, Ghostty, WezTerm,
// Warp, Kitty, Hyper, Alacritty), fall back to system Terminal.app via
// AppleScript. The AppleScript path is the only one that reliably opens a new
// window with the right cwd; `open -a Terminal $dir` works but is less
// dependable across versions.
//
// Linux: try $TERMINAL, then a list of common emulators. -e/--working-directory
// flags vary across emulators, so we cd into the directory inside the shell
// instead, which works universally.
//
// Windows: prefer Windows Terminal (`wt -d <dir>`), fall back to cmd.
func openTerminalAt(dir string) error {
	switch runtime.GOOS {
	case "darwin":
		return openTerminalDarwin(dir)
	case "windows":
		return openTerminalWindows(dir)
	default:
		return openTerminalLinux(dir)
	}
}

func openTerminalDarwin(dir string) error {
	// Escape double quotes for AppleScript.
	escaped := strings.ReplaceAll(dir, `"`, `\"`)
	script := `tell application "Terminal"
	activate
	do script "cd \"` + escaped + `\" && clear"
end tell`
	cmd := exec.Command("osascript", "-e", script)
	return cmd.Start()
}

func openTerminalLinux(dir string) error {
	candidates := []struct {
		name string
		args []string
	}{
		{"x-terminal-emulator", []string{"--working-directory=" + dir}},
		{"gnome-terminal", []string{"--working-directory=" + dir}},
		{"konsole", []string{"--workdir", dir}},
		{"xfce4-terminal", []string{"--working-directory=" + dir}},
		{"alacritty", []string{"--working-directory", dir}},
		{"kitty", []string{"--directory", dir}},
		{"foot", []string{"--working-directory", dir}},
		{"xterm", []string{"-e", "sh", "-c", "cd " + shellQuote(dir) + " && exec ${SHELL:-sh}"}},
	}
	if t := os.Getenv("TERMINAL"); t != "" {
		candidates = append([]struct {
			name string
			args []string
		}{{t, []string{"--working-directory=" + dir}}}, candidates...)
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		cmd := exec.Command(c.name, c.args...)
		if err := cmd.Start(); err == nil {
			return nil
		}
	}
	return errors.New("no supported terminal emulator found (set $TERMINAL)")
}

func openTerminalWindows(dir string) error {
	if _, err := exec.LookPath("wt"); err == nil {
		cmd := exec.Command("wt", "-d", dir)
		if err := cmd.Start(); err == nil {
			return nil
		}
	}
	cmd := exec.Command("cmd", "/c", "start", "cmd", "/K", "cd /d "+dir)
	return cmd.Start()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
