package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ClientID identifies a supported MCP client application.
type ClientID string

const (
	ClaudeDesktop ClientID = "claude_desktop"
	ClaudeCode    ClientID = "claude_code"
	Cursor        ClientID = "cursor"
	Windsurf      ClientID = "windsurf"
	Codex         ClientID = "codex"
	OpenCode      ClientID = "opencode"
	GeminiCLI     ClientID = "gemini_cli"
	GrokCLI       ClientID = "grok"
	MiMoCode      ClientID = "mimocode"
	Picoclaw      ClientID = "picoclaw"
	PiCLI         ClientID = "pi_cli"
)

// ClientInfo describes a single MCP client and its install status.
type ClientInfo struct {
	ID         ClientID `json:"id"`
	Name       string   `json:"name"`
	ConfigPath string   `json:"config_path"`
	Detected   bool     `json:"detected"`   // parent dir exists
	Configured bool     `json:"configured"` // "mcplexer" key present (or legacy "mx")
}

// StatusResult is the response for the status endpoint.
type StatusResult struct {
	Clients     []ClientInfo   `json:"clients"`
	BinaryPath  string         `json:"binary_path"`
	ServerEntry map[string]any `json:"server_entry"`
}

// PreviewResult is the response for the preview endpoint.
type PreviewResult struct {
	ConfigPath string `json:"config_path"`
	Content    string `json:"content"`
}

type clientDef struct {
	ID         ClientID
	Name       string
	configPath func(home string) string
}

var knownClients = []clientDef{
	{ClaudeDesktop, "Claude Desktop", claudeDesktopPath},
	{ClaudeCode, "Claude Code", claudeCodePath},
	{Cursor, "Cursor", cursorPath},
	{Windsurf, "Windsurf", windsurfPath},
	{Codex, "Codex", codexPath},
	{OpenCode, "OpenCode", openCodePath},
	{GeminiCLI, "Gemini CLI", geminiPath},
	{GrokCLI, "Grok CLI", grokPath},
	{MiMoCode, "MiMoCode", mimoCodePath},
	{Picoclaw, "Picoclaw", picoclawPath},
}

const (
	serverName       = "mcplexer"
	legacyServerName = "mx"
)

// Manager handles MCP client installation and detection.
type Manager struct {
	home       string
	exePath    string
	socketPath string
}

// New creates a Manager, resolving the home directory and binary path.
func New() (*Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "mcplexer"
	}
	// Prefer stable installed binary path if it exists.
	stablePath := filepath.Join(home, ".mcplexer", "bin", "mcplexer")
	if _, err := os.Stat(stablePath); err == nil {
		exe = stablePath
	}

	return &Manager{home: home, exePath: exe, socketPath: DefaultSocketPath()}, nil
}

// Status returns detection and configuration info for all known clients.
func (m *Manager) Status() (*StatusResult, error) {
	var clients []ClientInfo
	for _, c := range knownClients {
		ci := m.clientInfo(c)
		clients = append(clients, ci)
	}
	return &StatusResult{
		Clients:     clients,
		BinaryPath:  m.exePath,
		ServerEntry: m.serverEntry(),
	}, nil
}

// Install writes the MCPlexer server entry into a client's config file.
func (m *Manager) Install(id ClientID) (*ClientInfo, error) {
	def, err := m.findClient(id)
	if err != nil {
		return nil, err
	}
	ci := m.clientInfo(def)
	if !ci.Detected {
		return nil, fmt.Errorf("client %q not detected (config dir does not exist)\n  Install the client first, or run: mcplexer setup", id)
	}

	if err := m.mergeMCPConfig(def.ID, ci.ConfigPath); err != nil {
		return nil, fmt.Errorf("write config to %s: %w", ci.ConfigPath, err)
	}

	ci.Configured = true
	return &ci, nil
}

// Uninstall removes the MCPlexer server entry from a client's config file.
func (m *Manager) Uninstall(id ClientID) (*ClientInfo, error) {
	def, err := m.findClient(id)
	if err != nil {
		return nil, err
	}
	ci := m.clientInfo(def)
	if !ci.Configured {
		return nil, fmt.Errorf("client %q is not configured (no mcplexer entry found)", id)
	}

	if err := m.removeMCPConfig(def.ID, ci.ConfigPath); err != nil {
		return nil, fmt.Errorf("update config at %s: %w", ci.ConfigPath, err)
	}

	ci.Configured = false
	return &ci, nil
}

// Preview returns what the config file would look like after install.
func (m *Manager) Preview(id ClientID) (*PreviewResult, error) {
	def, err := m.findClient(id)
	if err != nil {
		return nil, err
	}
	ci := m.clientInfo(def)
	if !ci.Detected {
		return nil, fmt.Errorf("client %q not detected", id)
	}

	merged, err := m.previewMerge(def.ID, ci.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("preview: %w", err)
	}
	return &PreviewResult{ConfigPath: ci.ConfigPath, Content: merged}, nil
}

func (m *Manager) findClient(id ClientID) (clientDef, error) {
	for _, c := range knownClients {
		if c.ID == id {
			return c, nil
		}
	}
	return clientDef{}, fmt.Errorf("unknown client %q", id)
}

func (m *Manager) clientInfo(c clientDef) ClientInfo {
	path := c.configPath(m.home)
	detected := false
	configured := false

	if path != "" {
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); err == nil {
			detected = true
		}
		if detected {
			configured = m.hasConfigured(c.ID, path)
		}
	}

	return ClientInfo{
		ID:         c.ID,
		Name:       c.Name,
		ConfigPath: path,
		Detected:   detected,
		Configured: configured,
	}
}

func (m *Manager) hasConfigured(id ClientID, path string) bool {
	if id == GrokCLI {
		return hasGrokMCPConfig(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false // corrupted config treated as "not configured"
	}
	key := configMCPKey(id)
	servers, ok := cfg[key].(map[string]any)
	if !ok {
		return false
	}
	if _, exists := servers[serverName]; exists {
		return true
	}
	_, exists := servers[legacyServerName]
	return exists
}

func (m *Manager) serverEntry() map[string]any {
	return map[string]any{
		"command": m.exePath,
		"args":    []string{"connect", "--socket=" + m.resolvedSocketPath()},
	}
}

// configMCPKey returns the top-level JSON key that holds MCP server entries.
func configMCPKey(id ClientID) string {
	if id == OpenCode || id == MiMoCode {
		return "mcp"
	}
	return "mcpServers"
}

// serverEntryFor returns the server config entry in the format expected by the client.
func (m *Manager) serverEntryFor(id ClientID) map[string]any {
	if id == OpenCode {
		return map[string]any{
			"type":    "local",
			"command": []string{m.exePath, "connect", "--socket=" + m.resolvedSocketPath()},
		}
	}
	if id == MiMoCode {
		return map[string]any{
			"type":    "local",
			"command": []string{m.exePath, "connect", "--socket=" + m.resolvedSocketPath()},
			"enabled": true,
		}
	}
	return m.serverEntry()
}

// ServerEntryJSON returns the full mcpServers snippet as formatted JSON.
func (m *Manager) ServerEntryJSON() string {
	entry := map[string]any{
		"mcpServers": map[string]any{
			serverName: m.serverEntry(),
		},
	}
	out, _ := json.MarshalIndent(entry, "", "  ")
	return string(out)
}

func (m *Manager) mergeMCPConfig(id ClientID, path string) error {
	if id == GrokCLI {
		return mergeGrokMCPConfig(path, m.exePath, m.resolvedSocketPath())
	}
	cfg, err := m.readOrCreateConfig(path)
	if err != nil {
		return err
	}

	key := configMCPKey(id)
	servers, ok := cfg[key].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}
	servers[serverName] = m.serverEntryFor(id)
	delete(servers, legacyServerName)
	cfg[key] = servers

	return m.writeConfig(path, cfg)
}

func (m *Manager) removeMCPConfig(id ClientID, path string) error {
	if id == GrokCLI {
		return removeGrokMCPConfig(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config at %s: %w; config appears corrupted, delete it to regenerate or fix the JSON syntax", path, err)
	}

	key := configMCPKey(id)
	servers, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	delete(servers, serverName)
	delete(servers, legacyServerName)
	cfg[key] = servers

	return m.writeConfig(path, cfg)
}

func (m *Manager) previewMerge(id ClientID, path string) (string, error) {
	if id == GrokCLI {
		return previewGrokMCPConfig(path, m.exePath, m.resolvedSocketPath())
	}
	cfg, err := m.readOrCreateConfig(path)
	if err != nil {
		return "", err
	}

	key := configMCPKey(id)
	servers, ok := cfg[key].(map[string]any)
	if !ok {
		servers = make(map[string]any)
	}
	servers[serverName] = m.serverEntryFor(id)
	delete(servers, legacyServerName)
	cfg[key] = servers

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (m *Manager) resolvedSocketPath() string {
	if m.socketPath != "" {
		return m.socketPath
	}
	return DefaultSocketPath()
}

func DefaultSocketPath() string {
	if v := os.Getenv("MCPLEXER_SOCKET_PATH"); v != "" {
		return v
	}
	if runtime.GOOS == "linux" {
		if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
			return filepath.Join(runtimeDir, "mcplexer.sock")
		}
	}
	return "/tmp/mcplexer.sock"
}

func (m *Manager) readOrCreateConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse existing config at %s: %w; config appears corrupted, delete it to regenerate or fix the JSON syntax", path, err)
	}
	return cfg, nil
}

func (m *Manager) writeConfig(path string, cfg map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(path, append(out, '\n'), 0o644)
}

// Path functions — one per supported client.

func claudeDesktopPath(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "linux":
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	default:
		return ""
	}
}

func claudeCodePath(home string) string {
	// Claude Code reads MCP servers from ~/.claude.json (user scope), not
	// ~/.claude/settings.json. Writing to settings.json registers nothing.
	return filepath.Join(home, ".claude.json")
}

func cursorPath(home string) string {
	return filepath.Join(home, ".cursor", "mcp.json")
}

func windsurfPath(home string) string {
	return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
}

func codexPath(home string) string {
	return filepath.Join(home, ".codex", "mcp.json")
}

func openCodePath(home string) string {
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

func geminiPath(home string) string {
	return filepath.Join(home, ".gemini", "settings.json")
}

func grokPath(home string) string {
	return filepath.Join(home, ".grok", "config.toml")
}

func mimoCodePath(home string) string {
	return filepath.Join(home, ".config", "mimocode", "mimocode.json")
}

// picoclawPath returns the path to picoclaw's MCP configuration file.
// Picoclaw is Sipeed's tiny Go MCP agent (~10MB RSS on Pi Zero 2 W) and
// supports `picoclaw mcp add` plus a settings.json with an mcpServers
// block, mirroring Claude Code's layout. Defaulting to ~/.picoclaw/
// settings.json — adjust here if upstream picoclaw documentation
// converges on a different location.
func picoclawPath(home string) string {
	return filepath.Join(home, ".picoclaw", "settings.json")
}
