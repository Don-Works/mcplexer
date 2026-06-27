package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/harnesssync"
	"github.com/don-works/mcplexer/internal/install"
)

func cmdSetup() error {
	reader := bufio.NewReader(os.Stdin)

	// 1. Start daemon if not running
	dir, err := dataDir()
	if err != nil {
		return err
	}
	pid, ok := readPID(dir)
	if !ok || !processAlive(pid) {
		fmt.Println("Starting MCPlexer daemon...")
		if err := daemonStart(nil); err != nil {
			return fmt.Errorf("start daemon: %w", err)
		}
	} else {
		fmt.Printf("MCPlexer daemon already running (PID %d)\n", pid)
	}

	// 2. Install binary to stable path before configuring MCP clients
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if err := installBinary(exe); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	httpBaseURL := setupHTTPBaseURL()

	// 3. Detect installed MCP clients (now finds stable binary)
	mgr, err := install.New()
	if err != nil {
		return fmt.Errorf("init install manager: %w", err)
	}

	status, err := mgr.Status()
	if err != nil {
		return fmt.Errorf("detect clients: %w", err)
	}

	var detected []install.ClientInfo
	for _, c := range status.Clients {
		if c.Detected {
			detected = append(detected, c)
		}
	}

	if len(detected) == 0 {
		fmt.Println("\nNo MCP clients detected. Add this to your MCP client config manually:")
		fmt.Println(mgr.ServerEntryJSON())
	} else {
		fmt.Println("\nDetected MCP clients:")
		for _, c := range detected {
			fmt.Printf("  • %s\n", c.Name)
		}

		fmt.Print("\nConfigure MCPlexer for these clients? [Y/n] ")
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer == "" || answer == "y" || answer == "yes" {
			for _, c := range detected {
				if _, err := mgr.Install(c.ID); err != nil {
					fmt.Printf("  ✗ %s: %v\n", c.Name, err)
				} else {
					fmt.Printf("  ✓ %s\n", c.Name)
				}
			}
			fmt.Println("Restart your MCP clients to pick up the changes.")
		} else {
			fmt.Println("Skipped. Add this to your MCP client config manually:")
			fmt.Println(mgr.ServerEntryJSON())
		}
	}

	// 4. Offer launchd installation (macOS only)
	if runtime.GOOS == "darwin" && !launchdInstalled() {
		fmt.Print("\nInstall as launchd service (survives reboots)? [Y/n] ")
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "" || answer == "y" || answer == "yes" {
			daemonStop() //nolint:errcheck

			if err := installLaunchd(exe, "127.0.0.1:3333", "/tmp/mcplexer.sock"); err != nil {
				return fmt.Errorf("install launchd: %w", err)
			}
			fmt.Println("Launchd agent installed. MCPlexer will start automatically on boot.")
		}
	}

	// 4.5 Offer Shell Guard hook install for Claude Code (cooperative
	// mode — adds the PreToolUse curl bridge to ~/.claude/settings.json
	// so Bash invocations route through mcplexer's approval pipeline).
	// Done via the local HTTP API so we share the dashboard's audited
	// code path (records an InstallReceipt, flips installed_clients,
	// rewrites stale commands).
	for _, c := range detected {
		if c.ID != install.ClaudeCode {
			continue
		}
		fmt.Print("\nInstall Shell Guard PreToolUse hook for Claude Code? " +
			"(routes every Bash command through mcplexer for human approval) [Y/n] ")
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "" || answer == "y" || answer == "yes" {
			if err := installShellHookViaAPI(dir, httpBaseURL); err != nil {
				fmt.Printf("  ✗ shell hook install: %v\n", err)
			} else {
				fmt.Println("  ✓ Claude Code Shell Guard hook installed")
			}
		}
		break
	}

	// 5. Sync the canonical slim bootstrap into detected coding harnesses.
	// This replaces the old command/skill symlink installer: only Claude gets
	// a using-mcplexer SKILL.md sidecar, and every deeper playbook is fetched
	// dynamically from the registry through search_tools + execute_code.
	fmt.Print("\nSync the slim MCPlexer bootstrap into detected coding harnesses? [Y/n] ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || answer == "y" || answer == "yes" {
		syncHarnessBootstraps(detected)
	}

	// 6. Open browser (best effort)
	fmt.Printf("\nSetup complete. Open %s to manage MCPlexer.\n", httpBaseURL)
	openBrowser(httpBaseURL)
	return nil
}

func syncHarnessBootstraps(clients []install.ClientInfo) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("  ✗ resolve home: %v\n", err)
		return
	}
	seen := make(map[harnesssync.HarnessKey]bool)
	synced := 0
	for _, c := range clients {
		k, ok := harnessKeyForClientID(c.ID)
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		changed, _, err := harnesssync.Install(home, k, 1)
		if err != nil {
			fmt.Printf("  ✗ %s bootstrap: %v\n", k, err)
			continue
		}
		p := harnesssync.TargetPath(home, k)
		if changed {
			fmt.Printf("  ✓ synced %s bootstrap -> %s\n", k, p)
		} else {
			fmt.Printf("  ✓ %s bootstrap already current at %s\n", k, p)
		}
		if k == harnesssync.Claude {
			fmt.Printf("    using-mcplexer skill sidecar: %s\n", harnesssync.ClaudeSkillPath(home))
		}
		if k == harnesssync.OpenCode {
			fmt.Printf("    using-mcplexer skill sidecar: %s\n", harnesssync.OpenCodeSkillPath(home))
		}
		synced++
	}
	if synced == 0 {
		fmt.Println("  ! no supported coding harnesses detected for bootstrap sync")
	}
}

func harnessKeyForClientID(id install.ClientID) (harnesssync.HarnessKey, bool) {
	switch id {
	case install.ClaudeCode:
		return harnesssync.Claude, true
	case install.Codex:
		return harnesssync.Codex, true
	case install.OpenCode:
		return harnesssync.OpenCode, true
	case install.GeminiCLI:
		return harnesssync.Gemini, true
	case install.GrokCLI:
		return harnesssync.Grok, true
	case install.MiMoCode:
		return harnesssync.MiMo, true
	default:
		return "", false
	}
}

// installShellHookViaAPI POSTs to the local daemon's shell-guard install
// endpoint. We pick this over calling install.HookInstaller directly
// because the API path is what the dashboard uses — it records an
// InstallReceipt, flips installed_clients, and triggers the same
// drift-rewrite branch when re-installing over a stale hook. Reading
// the bearer token from ~/.mcplexer/api-key keeps this honest about
// the auth model; the daemon must already be running (cmdSetup ensures
// that earlier).
func installShellHookViaAPI(dataDir, baseURL string) error {
	tokenBytes, err := os.ReadFile(filepath.Join(dataDir, "api-key"))
	if err != nil {
		return fmt.Errorf("read api key: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return fmt.Errorf("api key file empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	baseURL = strings.TrimRight(baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/api/v1/guards/shell/clients/claude_code/install_hooks", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("daemon unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Error != "" {
			return fmt.Errorf("status %d: %s", resp.StatusCode, body.Error)
		}
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func setupHTTPBaseURL() string {
	candidates := setupHTTPBaseCandidates()
	if len(candidates) == 0 {
		return "http://127.0.0.1:3333"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	for _, base := range candidates {
		if probeHealthEndpoint(ctx, strings.TrimRight(base, "/")+"/healthz") {
			return base
		}
	}
	return candidates[0]
}

func setupHTTPBaseCandidates() []string {
	var out []string
	seen := make(map[string]bool)
	add := func(addr string) {
		u := setupClientURLFromAddr(addr)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	add(os.Getenv("MCPLEXER_HTTP_ADDR"))
	if runtime.GOOS == "darwin" {
		add(readLaunchdAddr())
	}
	add("127.0.0.1:3333")
	add("127.0.0.1:13333")
	return out
}

func setupClientURLFromAddr(addr string) string {
	a := strings.TrimSpace(addr)
	if a == "" {
		return ""
	}
	if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
		return strings.TrimRight(a, "/")
	}
	host, port, err := net.SplitHostPort(a)
	if err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	if strings.HasPrefix(a, ":") {
		return "http://127.0.0.1" + a
	}
	return httpURLFromAddr(a)
}

func readLaunchdAddr() string {
	b, err := os.ReadFile(launchdPlistPath())
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if _, after, ok := strings.Cut(line, "--addr="); ok {
			after = strings.TrimSpace(after)
			if strings.HasPrefix(after, "<") {
				continue
			}
			if end := strings.IndexAny(after, "<\"' "); end >= 0 {
				after = after[:end]
			}
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() //nolint:errcheck
		return
	default:
		return
	}
	exec.Command(cmd, url).Start() //nolint:errcheck
}
