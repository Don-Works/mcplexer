package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds application configuration loaded from environment variables.
type Config struct {
	Mode       string     // "stdio" or "http"
	HTTPAddr   string     // "127.0.0.1:3333"
	DBDriver   string     // "sqlite" or "postgres"
	DBDSN      string     // file path or connection string
	AgeKeyPath string     // path to age identity file
	ConfigFile string     // path to mcplexer.yaml
	LogLevel   slog.Level // slog level
	// LogPath, when non-empty, routes slog output through a rotating
	// lumberjack writer pointed at this file (mode 0600). When empty
	// (default for stdio mode, dev runs, tests), slog writes to stderr.
	// Daemon mode populates this so the on-disk log rotates instead of
	// growing unboundedly.
	LogPath        string
	SocketPath     string // unix socket path for multi-client mode
	ExternalURL    string // external URL for OAuth callbacks
	PublicURL      string // canonical browser URL for HTTPS/PWA installs
	WebPushSubject string // VAPID subject used for browser Web Push
	APITokenPath   string // path to HTTP API auth token (~/.mcplexer/api-key)
	// ServerProfile reshapes the daemon for appliance-style deployments.
	// "full" preserves the local workstation UI. "skills", "tasks", and
	// "skills+tasks" keep the shared server surfaces prominent.
	ServerProfile string
	// TrustedHosts lists extra hostnames allowed as browser Origin / CORS
	// targets in addition to loopback. Use this when serving the UI on a
	// non-localhost interface (e.g. binding to 0.0.0.0 and hitting
	// http://my-host:13333 in a browser). Comma-separated; bare hostnames
	// (no scheme/port) are matched against the request Origin's hostname.
	TrustedHosts []string

	// P2PEnabled toggles the embedded libp2p Host (R0.1 spike). Defaults off.
	// When false, the daemon's behavior is unchanged from a build without
	// libp2p support.
	P2PEnabled bool
	// P2PIdentityPath overrides the default identity location
	// (~/.mcplexer/p2p/identity.key). Spike-only: stored in cleartext.
	P2PIdentityPath string

	// brw browser-profile auto-discovery (DEFAULT OFF). When BrwAutodiscover
	// is set, the running gateway keeps its source="brw" downstream servers +
	// routes in sync with the live `brwctl daemons` roster — a file_watch on
	// the policy file plus a periodic interval fallback both reconcile, so
	// launching a brwd for a new profile makes its namespace appear with no
	// manual command. Off-by-default leaves existing deployments byte-for-byte
	// unchanged.
	BrwAutodiscover bool
	// BrwWorkspaces are the workspace IDs that get an allow route per daemon.
	// Empty = sync servers only (no routes).
	BrwWorkspaces []string
	// BrwPolicyPath is the browser-profiles.json to watch (file_watch trigger)
	// and pass as --profile-policy. Empty falls back to the canonical brw
	// default path for the watch; the reconcile itself relies on brwctl's own
	// discovery regardless.
	BrwPolicyPath string
	// BrwInterval is the periodic reconcile fallback cadence. Default 5m.
	BrwInterval time.Duration
	// BrwctlPath is the brwctl binary used to enumerate the roster.
	BrwctlPath string
	// BrwPrune deletes source="brw" servers/routes absent from the live roster
	// on each reconcile. Default off (a transient brwctl failure must not nuke
	// a working namespace).
	BrwPrune bool
}

// defaultDataPath returns ~/.mcplexer/<filename>, falling back to
// a CWD-relative path if the home directory can't be resolved.
func defaultDataPath(filename string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filename
	}
	return filepath.Join(home, ".mcplexer", filename)
}

func loadConfig() (*Config, error) {
	profile, err := normalizeServerProfile(envOr("MCPLEXER_SERVER_PROFILE", "full"))
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Mode:           envOr("MCPLEXER_MODE", "stdio"),
		HTTPAddr:       envOr("MCPLEXER_HTTP_ADDR", "127.0.0.1:3333"),
		DBDriver:       envOr("MCPLEXER_DB_DRIVER", "sqlite"),
		DBDSN:          envOr("MCPLEXER_DB_DSN", defaultDataPath("mcplexer.db")),
		AgeKeyPath:     envOr("MCPLEXER_AGE_KEY", ""),
		ConfigFile:     envOr("MCPLEXER_CONFIG", defaultDataPath("mcplexer.yaml")),
		LogLevel:       parseLogLevel(envOr("MCPLEXER_LOG_LEVEL", "info")),
		LogPath:        envOr("MCPLEXER_LOG_PATH", ""),
		SocketPath:     envOr("MCPLEXER_SOCKET_PATH", ""),
		ExternalURL:    envOr("MCPLEXER_EXTERNAL_URL", ""),
		PublicURL:      envOr("MCPLEXER_PUBLIC_URL", envOr("MCPLEXER_EXTERNAL_URL", "")),
		WebPushSubject: envOr("MCPLEXER_WEB_PUSH_SUBJECT", ""),
		APITokenPath:   envOr("MCPLEXER_API_TOKEN_PATH", defaultDataPath("api-key")),
		ServerProfile:  profile,

		TrustedHosts: mergeTrustedHosts(
			parseTrustedHosts(envOr("MCPLEXER_TRUSTED_HOSTS", "")),
			mergeTrustedHosts(localHostnames(), hostFromURL(envOr("MCPLEXER_PUBLIC_URL", envOr("MCPLEXER_EXTERNAL_URL", "")))),
		),

		P2PEnabled:      envBool("MCPLEXER_P2P_ENABLED", false),
		P2PIdentityPath: envOr("MCPLEXER_P2P_IDENTITY", ""),

		BrwAutodiscover: envBool("MCPLEXER_BRW_AUTODISCOVER", false),
		BrwWorkspaces:   parseCSVList(envOr("MCPLEXER_BRW_WORKSPACES", "")),
		BrwPolicyPath:   envOr("MCPLEXER_BRW_POLICY", ""),
		BrwInterval:     envDuration("MCPLEXER_BRW_INTERVAL", 5*time.Minute),
		BrwctlPath:      envOr("MCPLEXER_BRWCTL_PATH", "brwctl"),
		BrwPrune:        envBool("MCPLEXER_BRW_PRUNE", false),
	}
	return cfg, nil
}

// parseCSVList splits a comma-separated value into trimmed, non-empty
// entries. Returns nil for an empty/blank input.
func parseCSVList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// envDuration reads a Go duration env var, falling back when unset, empty,
// unparseable, or non-positive.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func hostFromURL(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	h := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if h == "" {
		return nil
	}
	return []string{h}
}

const (
	serverProfileFull        = "full"
	serverProfileSkills      = "skills"
	serverProfileTasks       = "tasks"
	serverProfileSkillsTasks = "skills+tasks"
)

func normalizeServerProfile(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	if p == "" {
		return serverProfileFull, nil
	}
	p = strings.ReplaceAll(p, ",", "+")
	p = strings.ReplaceAll(p, " ", "")
	switch p {
	case serverProfileFull:
		return serverProfileFull, nil
	case serverProfileSkills:
		return serverProfileSkills, nil
	case serverProfileTasks:
		return serverProfileTasks, nil
	case serverProfileSkillsTasks, "tasks+skills":
		return serverProfileSkillsTasks, nil
	default:
		return "", fmt.Errorf("server profile must be one of: full, skills, tasks, skills+tasks")
	}
}

func serverCapabilities(profile string) map[string]bool {
	profile, err := normalizeServerProfile(profile)
	if err != nil {
		profile = serverProfileFull
	}
	full := profile == serverProfileFull
	skills := full || profile == serverProfileSkills || profile == serverProfileSkillsTasks
	tasks := full || profile == serverProfileTasks || profile == serverProfileSkillsTasks
	local := full
	return map[string]bool{
		"approvals":       full,
		"audit":           full,
		"brain":           full,
		"delegations":     full,
		"downstreams":     full,
		"guards":          full,
		"local_setup":     local,
		"memory":          full,
		"model_routing":   full,
		"server_settings": true,
		"signals":         true,
		"skills":          skills,
		"tasks":           tasks,
		"workers":         full,
	}
}

// localHostnames returns the daemon's own hostname plus every sensible
// alias a user might type in a browser to reach the same machine.
// macOS in particular advertises three different names:
//
//   - the short hostname              ("my-mac")        — what people type
//   - the mDNS / Bonjour form         ("my-mac.local")  — LAN discovery
//   - the full domain-qualified form  ("my-mac.ts.net") — Tailscale, VPNs
//
// os.Hostname() only returns one of these — whichever the kernel was
// told to use most recently — so we have to derive the other shapes
// ourselves. The short form is everything before the first dot; the
// mDNS form is `<short>.local`. All three are trusted.
//
// Failures are silent: env-driven MCPLEXER_TRUSTED_HOSTS still works,
// and loopback is always trusted regardless.
func localHostnames() []string {
	raw, err := os.Hostname()
	if err != nil {
		return nil
	}
	// macOS in particular can return a whitespace-separated list of
	// names from gethostname() when Tailscale or other tooling has
	// stamped multiple aliases (e.g. "my-mac.ts.net lan"). Split + add
	// each one so every variant is trusted.
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, tok := range strings.Fields(raw) {
		add(tok)
		short := tok
		if i := strings.Index(tok, "."); i > 0 {
			short = tok[:i]
		}
		add(short)
		if short != "" && !strings.HasSuffix(short, ".local") {
			add(short + ".local")
		}
	}
	return out
}

// mergeTrustedHosts unions two host lists case-insensitively while
// preserving the order of `primary` first, then any new entries from
// `extra`. Used to fold the daemon's own hostname into whatever the
// operator set in MCPLEXER_TRUSTED_HOSTS without forcing them to repeat
// it.
func mergeTrustedHosts(primary, extra []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(extra))
	out := make([]string, 0, len(primary)+len(extra))
	for _, h := range primary {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	for _, h := range extra {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseTrustedHosts splits a comma-separated host list, normalizes entries to
// bare lower-case hostnames, and drops empties. Operators often paste full
// browser origins ("https://host:3333/app") even though the runtime matches
// only Origin.Hostname(); accept those forms so a harmless formatting mismatch
// does not break mobile PWA/CORS access.
func parseTrustedHosts(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		h := normalizeConfiguredHost(p)
		if h == "" {
			continue
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeConfiguredHost(raw string) string {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
			return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		}
		return ""
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if parsed, err := url.Parse("http://" + h); err == nil && parsed.Hostname() != "" {
		return strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.Trim(strings.TrimSuffix(h, "."), "[]"))
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	switch v {
	case "1", "true", "TRUE", "True", "yes", "on":
		return true
	case "0", "false", "FALSE", "False", "no", "off":
		return false
	default:
		return fallback
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
