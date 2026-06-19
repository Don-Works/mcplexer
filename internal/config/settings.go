package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// DisplayName is purely cosmetic / UX. It is NOT auth-bearing — cryptographic
// trust still rides on the libp2p PeerID. We pass DisplayName through pairing
// + mesh envelopes as a hint so users see "max-mbp" instead of "peer-Ymq…"
// in lists and audit lines. Receivers MUST NOT use DisplayName for any trust
// or routing decision — those are keyed strictly off PeerID.
const (
	displayNameMinLen  = 1
	displayNameMaxLen  = 50
	displayNamePattern = `^[a-zA-Z0-9._-]+$`
)

// displayNameRE validates the DisplayName surface (alnum + . _ -).
var displayNameRE = regexp.MustCompile(displayNamePattern)

// Settings holds user-configurable runtime settings.
type Settings struct {
	SlimTools bool `json:"slim_tools"`
	// SlimSurface, when true (default), restricts the static tools/list
	// response to a hand-picked keep-list of universally-needed entrypoints
	// (mcpx__execute_code, mcpx__search_tools, secret__prompt,
	// secret__list_refs). Every other mcplexer built-in (mesh__*, memory__*,
	// task__*, skill__*, mcpx__skill_*, …) remains callable + discoverable
	// via mcpx__search_tools, but doesn't pollute the agent's top-level
	// tool inventory. Saves ~22k tokens of MCP-tool context per session.
	// Set false (or MCPLEXER_SLIM_SURFACE=false) to advertise everything.
	SlimSurface             bool   `json:"slim_surface"`
	CompactResponses        bool   `json:"compact_responses"`
	ToolsCacheTTLSec        int    `json:"tools_cache_ttl_sec"`
	LogLevel                string `json:"log_level"`
	CodeModeTimeoutSec      int    `json:"code_mode_timeout_sec"`
	CodeModeMaxOutputBytes  int    `json:"code_mode_max_output_bytes"`
	MeshEnabled             bool   `json:"mesh_enabled"`
	MeshReceiveMaxResults   int    `json:"mesh_receive_max_results"`
	MeshReceivePreviewBytes int    `json:"mesh_receive_preview_bytes"`
	MeshSendMaxContentBytes int    `json:"mesh_send_max_content_bytes"`
	P2PEnabled              bool   `json:"p2p_enabled"`
	// SanitizerEnvelopeAlways forces every tool result through the
	// "untrusted-content" envelope, even when no denylist pattern hit.
	// When false (default), only matched content is enveloped. Wired
	// into gateway.handler_sanitize via h.settingsSvc.Load at call time.
	SanitizerEnvelopeAlways bool `json:"sanitizer_envelope_always"`
	// SandboxDownstreams, when true and the host's sandbox driver is
	// Available(), wraps every downstream MCP server spawn in
	// sandbox-exec / bwrap so credential paths under ~/.ssh, ~/.aws
	// etc. are inaccessible to the downstream process. Off by default
	// because some MCP servers legitimately need network or specific
	// FS access; the user opts in once they've reviewed their server
	// list.
	SandboxDownstreams bool `json:"sandbox_downstreams"`
	// DisplayName is the user-visible label for THIS device on the mesh and
	// in paired-peer lists on remote devices. NOT auth-bearing — it's purely
	// a UX hint; PeerID remains the only cryptographic trust anchor.
	DisplayName     string `json:"display_name"`
	TelegramEnabled bool   `json:"telegram_enabled"`
	// DangerousModeEnabled, when true, globally disables every approval
	// gate so a user blocked on critical work can blast through. The
	// audit trail stays intact — every would-have-gated call still emits
	// an audit row + notify event with status="dangerous-mode bypass"
	// so a follow-up review can reconstruct what was bypassed and why.
	// Sticky: persists across daemon restarts until the user explicitly
	// turns it back off. NOT scoped per-workspace (intentionally global —
	// this IS the escape hatch).
	DangerousModeEnabled bool `json:"dangerous_mode_enabled"`
	// PureMode, when true, makes the gateway an MCP passthrough for
	// local-only coding sessions: tools/list returns an empty surface
	// and tools/call denies every MCP dispatch. Defaults false. Recovery:
	// dashboard/settings API, or MCPLEXER_PURE_MODE=0/false env override
	// (env always wins).
	PureMode                  bool              `json:"pure_mode"`
	ToolDescriptionOverrides  map[string]string `json:"tool_description_overrides"`
	ToolHints                 map[string]string `json:"tool_hints"`
	DescriptionRefinementMode string            `json:"description_refinement_mode"`
	// MeshAutoReplicateOff opts THIS device OUT of Tier-1 silent memory
	// replication. When false (the default) a memory OFFERED by a
	// SameUser (Tier-1) paired peer is pulled automatically + silently
	// into the bound local workspace — the user's own machines stay in
	// sync without a manual mesh__request_memory. Set true to fall back
	// to the legacy OFFER-only behaviour (offers recorded, never auto-
	// imported). Only affects the SameUser tier; Tier-2/3 offers are
	// never auto-pulled regardless of this flag.
	MeshAutoReplicateOff bool `json:"mesh_auto_replicate_off"`
	// DelegationDisabledProviders holds operator-controlled disables for
	// whole delegation provider/subscription groups. When a key is true,
	// candidates whose provider or logical group matches the key are
	// excluded from capacity lists, ranked/routed delegation choices, and
	// future UI suggestions. Supported keys (and raw provider fallbacks):
	// "opencode", "local", "claude", "grok", "mimo", "pi", "openrouter", "minimax",
	// "opencode_cli", "claude_cli", "grok_cli", "mimo_cli", "pi_cli", "openai_compat", ...
	// Persisted via the normal settings row; backend-owned.
	DelegationDisabledProviders map[string]bool `json:"delegation_disabled_providers,omitempty"`
	// RemoteSkillServerURL is the canonical base URL for the central skills
	// registry/hub this daemon should sync with. Operators may type a bare DNS
	// name such as "skills.example"; Save normalizes it to
	// "http://skills.example".
	RemoteSkillServerURL string `json:"remote_skill_server_url,omitempty"`
	// AutoUpdateBootstrap, when true (default), automatically triggers
	// a bootstrap reinstall on the harness setup page when version drift
	// is detected, instead of requiring a manual click.
	AutoUpdateBootstrap bool `json:"auto_update_bootstrap"`
}

// DefaultSettings returns settings with sensible defaults.
func DefaultSettings() Settings {
	return Settings{
		SlimTools:                 true,
		SlimSurface:               true,
		CompactResponses:          true,
		ToolsCacheTTLSec:          15,
		LogLevel:                  "info",
		CodeModeTimeoutSec:        30,
		CodeModeMaxOutputBytes:    24 * 1024,
		MeshReceiveMaxResults:     20,
		MeshReceivePreviewBytes:   512,
		MeshSendMaxContentBytes:   64 * 1024,
		TelegramEnabled:           false,
		DisplayName:               defaultDisplayName(),
		DescriptionRefinementMode: "manual",
		ToolDescriptionOverrides:  map[string]string{},
		ToolHints: map[string]string{
			"postgres__query": "Query information_schema.tables and information_schema.columns to discover the schema before writing queries.",
		},
		DelegationDisabledProviders: map[string]bool{},
		AutoUpdateBootstrap:         true,
	}
}

// defaultDisplayName derives a friendly-ish label from the OS hostname:
// lowercased, short form (first dotted component), with anything outside
// the validation alphabet replaced with '-'. Empty hostname falls back to
// "device" so the field is never blank.
func defaultDisplayName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "device"
	}
	short := strings.ToLower(strings.Split(h, ".")[0])
	var b strings.Builder
	for _, r := range short {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "device"
	}
	if len(out) > displayNameMaxLen {
		out = out[:displayNameMaxLen]
	}
	return out
}

// ValidateDisplayName reports whether s is a valid DisplayName per the
// pairing/mesh contract: 1-50 chars, [a-zA-Z0-9._-]. Exposed for the
// pairing handler so it can reject malicious labels at the boundary.
func ValidateDisplayName(s string) error {
	if len(s) < displayNameMinLen || len(s) > displayNameMaxLen {
		return fmt.Errorf(
			"display_name must be %d-%d characters", displayNameMinLen, displayNameMaxLen)
	}
	if !displayNameRE.MatchString(s) {
		return fmt.Errorf(
			"display_name must contain only letters, numbers, '.', '_', '-'")
	}
	return nil
}

// BuiltinToolDefaults returns the hardcoded descriptions for all built-in tools.
func BuiltinToolDefaults() map[string]string {
	return map[string]string{
		"mcpx__search_tools":           "Search for tools by keyword. Infer search terms from the user's intent — don't ask what to search for, just guess. Returns matching tools grouped by query. Use detail: \"full\" for TypeScript signatures needed before writing execute_code scripts.",
		"mcpx__flush_cache":            "Flush the tool call cache to force fresh data on subsequent calls. Use this when you suspect cached data is stale or after making changes that should be reflected immediately. Optionally specify a server_id to flush only that server's cache. Note: you can also pass `_cache_bust: true` as an argument to any individual tool call to bypass the cache for that specific request without flushing the entire cache.",
		"mcpx__list_pending_approvals": "List pending tool call approvals waiting for review. Returns approval IDs, tool names, justifications, and requesting agent info. Your own pending requests are excluded.",
		"mcpx__approve_tool_call":      "Approve a pending tool call request. You cannot approve your own requests.",
		"mcpx__deny_tool_call":         "Deny a pending tool call request. You cannot deny your own requests. A reason is required.",
		"mcpx__execute_code":           "Execute JavaScript code that batches multiple tool calls into one invocation. ALWAYS batch related calls into a single script. Calls are synchronous (no await). NEVER print raw API responses — filter and summarize before printing. Use search_tools to find available functions. Use print() for output.",
		"mesh__send":                   "Send a message to the agent mesh for inter-agent communication. Use this to share findings, ask questions, assign tasks, or broadcast alerts to other agents working in the same workspace.",
		"mesh__receive":                "Receive messages from the agent mesh and discover active agents. Call this periodically to check for new messages from other agents. On first call, optionally set your name and role so other agents can identify you.",
		"mcpx__suggest_description":    "Suggest an improved description for a tool you have used. Provide the full namespaced tool name, the improved description, and a rationale explaining what you changed and why. Good suggestions clarify ambiguous behavior, add missing context, or correct inaccuracies you discovered while using the tool.",
	}
}

// SettingsService loads and saves settings, merging DB values with defaults
// and env var overrides.
type SettingsService struct {
	store store.SettingsStore
}

// NewSettingsService creates a SettingsService.
func NewSettingsService(s store.SettingsStore) *SettingsService {
	return &SettingsService{store: s}
}

// Load reads settings from the DB, merges with defaults, and applies env
// var overrides. Env vars take precedence over DB values.
func (s *SettingsService) Load(ctx context.Context) Settings {
	settings := DefaultSettings()

	raw, err := s.store.GetSettings(ctx)
	if err != nil {
		slog.Warn("failed to load settings from DB, using defaults", "error", err)
		return applyEnvOverrides(settings)
	}

	if len(raw) > 0 && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &settings); err != nil {
			slog.Warn("failed to parse settings JSON, using defaults", "error", err)
			settings = DefaultSettings()
		}
	}

	// Ensure maps are never nil.
	if settings.ToolDescriptionOverrides == nil {
		settings.ToolDescriptionOverrides = map[string]string{}
	}
	if settings.ToolHints == nil {
		settings.ToolHints = map[string]string{}
	}
	if settings.DelegationDisabledProviders == nil {
		settings.DelegationDisabledProviders = map[string]bool{}
	}
	// Legacy rows pre-display_name → fill from hostname so paired peers
	// get something friendlier than "peer-Ymq…" without the user lifting
	// a finger.
	if settings.DisplayName == "" {
		settings.DisplayName = defaultDisplayName()
	}
	if normalized, err := NormalizeRemoteSkillServerURL(settings.RemoteSkillServerURL); err == nil {
		settings.RemoteSkillServerURL = normalized
	}

	return applyEnvOverrides(settings)
}

// Save validates and persists settings to the DB.
func (s *SettingsService) Save(ctx context.Context, settings Settings) error {
	remote, err := NormalizeRemoteSkillServerURL(settings.RemoteSkillServerURL)
	if err != nil {
		return err
	}
	settings.RemoteSkillServerURL = remote
	if err := validateSettings(settings); err != nil {
		return err
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	return s.store.UpdateSettings(ctx, data)
}

func validateSettings(s Settings) error {
	if s.ToolsCacheTTLSec < 0 || s.ToolsCacheTTLSec > 300 {
		return fmt.Errorf("tools_cache_ttl_sec must be between 0 and 300")
	}

	validLevels := map[string]bool{
		"debug": true, "info": true, "warn": true, "error": true,
	}
	if !validLevels[strings.ToLower(s.LogLevel)] {
		return fmt.Errorf("log_level must be one of: debug, info, warn, error")
	}

	if s.CodeModeTimeoutSec < 1 || s.CodeModeTimeoutSec > 120 {
		return fmt.Errorf("code_mode_timeout_sec must be between 1 and 120")
	}
	if s.CodeModeMaxOutputBytes < 1024 || s.CodeModeMaxOutputBytes > 256*1024 {
		return fmt.Errorf("code_mode_max_output_bytes must be between 1024 and 262144")
	}
	if s.MeshReceiveMaxResults < 1 || s.MeshReceiveMaxResults > 50 {
		return fmt.Errorf("mesh_receive_max_results must be between 1 and 50")
	}
	if s.MeshReceivePreviewBytes < 64 || s.MeshReceivePreviewBytes > 2048 {
		return fmt.Errorf("mesh_receive_preview_bytes must be between 64 and 2048")
	}
	if s.MeshSendMaxContentBytes < 1024 || s.MeshSendMaxContentBytes > 64*1024 {
		return fmt.Errorf("mesh_send_max_content_bytes must be between 1024 and 65536")
	}

	validRefinement := map[string]bool{
		"off": true, "manual": true, "auto": true, "": true,
	}
	if !validRefinement[s.DescriptionRefinementMode] {
		return fmt.Errorf("description_refinement_mode must be one of: off, manual, auto")
	}

	if s.DisplayName != "" {
		if err := ValidateDisplayName(s.DisplayName); err != nil {
			return err
		}
	}
	if _, err := NormalizeRemoteSkillServerURL(s.RemoteSkillServerURL); err != nil {
		return err
	}

	return nil
}

// NormalizeRemoteSkillServerURL accepts either a full http(s) URL or a bare
// DNS name/host[:port]. Bare DNS names use the scheme default port so the
// public skills dashboard can live at http://skills.example while operators can
// still opt into a non-standard daemon port with host:port.
func NormalizeRemoteSkillServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	wasBare := !strings.Contains(raw, "://")
	if wasBare {
		if strings.ContainsAny(raw, "/?#") {
			return "", fmt.Errorf("remote_skill_server_url must be a DNS name, host:port, or http(s) URL")
		}
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("remote_skill_server_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("remote_skill_server_url must use http or https")
	}
	if u.User != nil {
		return "", fmt.Errorf("remote_skill_server_url must not include credentials")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("remote_skill_server_url must include a host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("remote_skill_server_url must not include query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), nil
}

// applyEnvOverrides lets env vars take precedence over DB values.
func applyEnvOverrides(s Settings) Settings {
	if v := os.Getenv("MCPLEXER_SLIM_SURFACE"); v != "" {
		s.SlimSurface = strings.ToLower(v) != "false"
	}
	if v := os.Getenv("MCPLEXER_SLIM_TOOLS"); v != "" {
		s.SlimTools = envBoolDefaultTrue(v)
	}
	if v := os.Getenv("MCPLEXER_LOG_LEVEL"); v != "" {
		s.LogLevel = strings.ToLower(v)
	}
	if v := os.Getenv("MCPLEXER_COMPACT_RESPONSES"); v != "" {
		s.CompactResponses = envBoolDefaultTrue(v)
	}
	if v := os.Getenv("MCPLEXER_CODE_MODE_MAX_OUTPUT_BYTES"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			s.CodeModeMaxOutputBytes = n
		}
	}
	if v := os.Getenv("MCPLEXER_MESH_RECEIVE_MAX_RESULTS"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			s.MeshReceiveMaxResults = n
		}
	}
	if v := os.Getenv("MCPLEXER_MESH_RECEIVE_PREVIEW_BYTES"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			s.MeshReceivePreviewBytes = n
		}
	}
	if v := os.Getenv("MCPLEXER_MESH_SEND_MAX_CONTENT_BYTES"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			s.MeshSendMaxContentBytes = n
		}
	}
	if v := os.Getenv("MCPLEXER_DESCRIPTION_REFINEMENT_MODE"); v != "" {
		s.DescriptionRefinementMode = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("MCPLEXER_REMOTE_SKILL_SERVER_URL"); v != "" {
		if normalized, err := NormalizeRemoteSkillServerURL(v); err == nil {
			s.RemoteSkillServerURL = normalized
		}
	}
	// MeshEnabled has no UI toggle today; the dashboard flips it via
	// PUT /api/v1/settings. The env override exists so headless test +
	// CI deployments (notably the multi-node integration harness) can
	// boot with mesh on without a follow-up PUT + restart dance.
	if v := os.Getenv("MCPLEXER_MESH_ENABLED"); v != "" {
		s.MeshEnabled = envBoolDefaultTrue(v)
	}
	// Opt-out for Tier-1 silent memory replication. Defaults OFF (auto-pull
	// ON); set MCPLEXER_MESH_AUTO_REPLICATE_OFF=1 to disable per-host
	// without a settings PUT (headless / CI parity with MeshEnabled).
	if v := os.Getenv("MCPLEXER_MESH_AUTO_REPLICATE_OFF"); v != "" {
		s.MeshAutoReplicateOff = envBoolDefaultTrue(v)
	}
	// PureMode defaults OFF; env wins so a headless operator can always
	// recover from a stale DB row with MCPLEXER_PURE_MODE=0.
	if v := os.Getenv("MCPLEXER_PURE_MODE"); v != "" {
		s.PureMode = envBoolDefaultTrue(v)
	}
	return s
}

func envBoolDefaultTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func parsePositiveInt(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("expected positive integer")
	}
	return n, nil
}
