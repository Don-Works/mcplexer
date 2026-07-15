// monitoring.go — domain models for the Monitoring feature (remote log
// intelligence, migration 128). RemoteHost is one SSH target the
// collector pulls docker logs from — READ-ONLY by construction, see
// docs/adr/0007-remote-exec-ssh-security-model.md. LogSource is one
// stream on a host. MonitoringChannel is one workspace-scoped alert
// output whose config carries secret:// refs only.
package store

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// Severity levels, ordered. SeverityRank turns one into a comparable
// int so channel min_severity floors are a single comparison.
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

var severityRanks = map[string]int{
	SeverityInfo: 0, SeverityWarn: 1, SeverityError: 2, SeverityCritical: 3,
}

// SeverityRank returns the ordinal for a severity, or -1 when unknown.
func SeverityRank(s string) int {
	r, ok := severityRanks[s]
	if !ok {
		return -1
	}
	return r
}

// ValidSeverity reports whether s is one of the four known levels.
func ValidSeverity(s string) bool { return SeverityRank(s) >= 0 }

// Monitoring channel kinds. Dispatch happens daemon-side in
// internal/logwatch/escalate — the log-watch worker holds no channel
// tools, so these rows are the ONLY way an alert leaves the gateway.
const (
	ChannelKindGChatWebhook = "gchat_webhook"
	ChannelKindTelegram     = "telegram"
	ChannelKindWhatsApp     = "whatsapp"
	ChannelKindMesh         = "mesh"
)

// Log source kinds. `docker logs` is the ratified collection contract;
// compose and swarm provide stable deploy-level selectors while
// journald/file cover third-party boxes (M6).
const (
	LogSourceKindDocker   = "docker"
	LogSourceKindCompose  = "compose"
	LogSourceKindSwarm    = "swarm"
	LogSourceKindJournald = "journald"
	LogSourceKindFile     = "file"
)

// RemoteHost is one SSH target. AuthScopeID points at the AuthScope
// holding the credential (ssh_key material or ssh_agent socket ref);
// key bytes never leave the secrets subsystem. HostKeyPin is the
// TOFU-recorded host public key fingerprint — empty until first dial,
// then any mismatch hard-fails the host (re-pin is an explicit
// operator action, never automatic).
type RemoteHost struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	SSHUser     string    `json:"ssh_user"`
	SSHHost     string    `json:"ssh_host"`
	SSHPort     int       `json:"ssh_port"`
	AuthScopeID string    `json:"auth_scope_id"`
	HostKeyPin  string    `json:"host_key_pin,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LogSource is one log stream on a RemoteHost. Selector is the docker
// container name (or journald unit / file path for later kinds) and is
// validated against a strict charset at CRUD time AND dial time — it
// is interpolated into a fixed argv template, never a shell string.
// CursorTS/CursorHash track incremental pulls; ConsecutiveFailures
// drives health surfacing + the source-went-dark anomaly rule.
type LogSource struct {
	ID                  string     `json:"id"`
	WorkspaceID         string     `json:"workspace_id"`
	RemoteHostID        string     `json:"remote_host_id"`
	Name                string     `json:"name"`
	Kind                string     `json:"kind"`
	Selector            string     `json:"selector"`
	ScheduleSpec        string     `json:"schedule_spec"`
	MaxPullBytes        int64      `json:"max_pull_bytes"`
	RetentionMB         int        `json:"retention_mb"`
	RetentionDays       int        `json:"retention_days"`
	SeverityRulesJSON   string     `json:"severity_rules_json,omitempty"`
	Enabled             bool       `json:"enabled"`
	CursorTS            *time.Time `json:"cursor_ts,omitempty"`
	CursorHash          string     `json:"cursor_hash,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// MonitoringChannel is one alert output. ConfigJSON carries secret://
// refs only (e.g. {"webhook_ref":"secret://GCHAT_WEBHOOK_INCIDENTS"});
// plaintext URLs/credentials are rejected at CRUD time. MinSeverity is
// the per-channel floor: an incident fans out to every enabled channel
// whose floor admits its severity.
type MonitoringChannel struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	ConfigJSON  string    `json:"config_json"`
	MinSeverity string    `json:"min_severity"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LogTemplate is one masked line shape per source: the distiller's
// dedup unit. Count is lifetime; WindowCount resets per digest window.
// Acked templates are excluded from novelty wake-ups.
type LogTemplate struct {
	ID          string    `json:"id"` // sha256(source_id, masked)
	SourceID    string    `json:"source_id"`
	Masked      string    `json:"masked"`
	Severity    string    `json:"severity"`
	Count       int64     `json:"count"`
	WindowCount int64     `json:"window_count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	SampleFirst string    `json:"sample_first"`
	SampleLast  string    `json:"sample_last"`
	Acked       bool      `json:"acked"`
	AckNote     string    `json:"ack_note,omitempty"`
}

// LogLine is one redacted raw line in the bounded ring buffer.
type LogLine struct {
	SourceID   string    `json:"source_id"`
	TemplateID string    `json:"template_id"`
	TS         time.Time `json:"ts"`
	Line       string    `json:"line"`
}

var (
	// First char may not be '-': a leading dash lets a selector like
	// "--follow" reach docker/journalctl's flag parser as an option rather
	// than a positional name (argument injection). Real container/project/
	// service/unit names never start with '-'.
	selectorRe  = regexp.MustCompile(`^[A-Za-z0-9._/][A-Za-z0-9._/-]*$`)
	sshHostRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	sshUserRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	secretRefRe = regexp.MustCompile(`^secret://[A-Za-z0-9_.-]+$`)
)

// ValidateSelector enforces the strict selector charset from ADR 0007.
// Called at CRUD time and again at dial time.
func ValidateSelector(selector string) error {
	if selector == "" || !selectorRe.MatchString(selector) {
		return &FieldError{
			Code: "invalid_selector", Field: "selector", Value: selector,
			Message: "selector must match ^[A-Za-z0-9._/][A-Za-z0-9._/-]*$ (no shell metacharacters, no leading dash)",
			Hint:    "use the plain docker container name, e.g. api or my-app-1",
		}
	}
	return nil
}

// ValidateRemoteHost checks the SSH coordinate fields.
func ValidateRemoteHost(h *RemoteHost) error {
	switch {
	case strings.TrimSpace(h.Name) == "":
		return &FieldError{Code: "missing_name", Field: "name", Message: "name is required"}
	case h.AuthScopeID == "":
		return &FieldError{Code: "missing_auth_scope", Field: "auth_scope_id", Message: "auth_scope_id is required"}
	case !sshUserRe.MatchString(h.SSHUser):
		return &FieldError{Code: "invalid_ssh_user", Field: "ssh_user", Value: h.SSHUser,
			Message: "ssh_user must match ^[A-Za-z0-9._-]+$"}
	case !sshHostRe.MatchString(h.SSHHost):
		return &FieldError{Code: "invalid_ssh_host", Field: "ssh_host", Value: h.SSHHost,
			Message: "ssh_host must be a hostname or IPv4 address (^[A-Za-z0-9._-]+$)"}
	case h.SSHPort < 1 || h.SSHPort > 65535:
		return &FieldError{Code: "invalid_ssh_port", Field: "ssh_port",
			Message: "ssh_port must be 1-65535"}
	}
	return nil
}

// channelRequiredRefs maps kind → config keys that MUST be secret://
// refs. Telegram's chat_id is an internal binding id, not a credential.
var channelRequiredRefs = map[string][]string{
	ChannelKindGChatWebhook: {"webhook_ref"},
	ChannelKindWhatsApp:     {"chat_id_ref"}, // openwa chat id (e.g. 44…@c.us) — PII, always a ref
	ChannelKindTelegram:     nil,
	ChannelKindMesh:         nil,
}

// ValidateMonitoringChannel enforces kind, severity floor, and the
// secrets rule: required *_ref keys must be secret:// refs and NO
// config value may embed a plaintext http(s) URL (webhook URLs are
// credentials — store them in the secrets subsystem).
func ValidateMonitoringChannel(c *MonitoringChannel) error {
	refs, ok := channelRequiredRefs[c.Kind]
	if !ok {
		return &FieldError{Code: "invalid_channel_kind", Field: "kind", Value: c.Kind,
			Message: "kind must be gchat_webhook|telegram|whatsapp|mesh"}
	}
	if !ValidSeverity(c.MinSeverity) {
		return &FieldError{Code: "invalid_min_severity", Field: "min_severity", Value: c.MinSeverity,
			Message: "min_severity must be info|warn|error|critical"}
	}
	cfg := map[string]any{}
	if c.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(c.ConfigJSON), &cfg); err != nil {
			return &FieldError{Code: "invalid_config_json", Field: "config_json",
				Message: "config_json must be a JSON object", Cause: err}
		}
	}
	for _, key := range refs {
		v, _ := cfg[key].(string)
		if !secretRefRe.MatchString(v) {
			return &FieldError{Code: "plaintext_channel_credential", Field: "config_json", Value: key,
				Message: key + " must be a secret:// ref, never a plaintext value",
				Hint:    "store the value via the secrets subsystem, then reference it as secret://<KEY>",
			}
		}
	}
	for k, v := range cfg {
		if s, isStr := v.(string); isStr && (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
			return &FieldError{Code: "plaintext_channel_credential", Field: "config_json", Value: k,
				Message: "config values must not embed plaintext URLs — use a secret:// ref",
			}
		}
	}
	return nil
}

// ValidateLogSource checks kind, selector, and bounds.
func ValidateLogSource(s *LogSource) error {
	switch s.Kind {
	case LogSourceKindDocker, LogSourceKindCompose, LogSourceKindSwarm, LogSourceKindJournald, LogSourceKindFile:
	default:
		return &FieldError{Code: "invalid_source_kind", Field: "kind", Value: s.Kind,
			Message: "kind must be docker|compose|swarm|journald|file (docker/compose/swarm/journald are collected; file needs byte-offset cursoring — tracked)"}
	}
	if strings.TrimSpace(s.Name) == "" {
		return &FieldError{Code: "missing_name", Field: "name", Message: "name is required"}
	}
	if s.RemoteHostID == "" {
		return &FieldError{Code: "missing_remote_host", Field: "remote_host_id", Message: "remote_host_id is required"}
	}
	return ValidateSelector(s.Selector)
}

// MonitoringStore manages remote_hosts, log_sources and
// monitoring_channels (migration 128). Template/line methods land with
// the distiller (M3). Sentinel errors: ErrRemoteHostNotFound,
// ErrLogSourceNotFound, ErrMonitoringChannelNotFound.
type MonitoringStore interface {
	CreateRemoteHost(ctx context.Context, h *RemoteHost) error
	GetRemoteHost(ctx context.Context, id string) (*RemoteHost, error)
	ListRemoteHosts(ctx context.Context, workspaceID string) ([]*RemoteHost, error)
	UpdateRemoteHost(ctx context.Context, h *RemoteHost) error
	DeleteRemoteHost(ctx context.Context, id string) error
	// SetRemoteHostPin records (or explicitly re-records) the TOFU
	// host-key fingerprint. Pass pin="" to clear before a deliberate
	// operator re-pin.
	SetRemoteHostPin(ctx context.Context, id, pin string) error

	CreateLogSource(ctx context.Context, s *LogSource) error
	GetLogSource(ctx context.Context, id string) (*LogSource, error)
	ListLogSources(ctx context.Context, workspaceID string) ([]*LogSource, error)
	// ListEnabledLogSources spans all workspaces — the collector's
	// scheduling view (host join happens in the collector).
	ListEnabledLogSources(ctx context.Context) ([]*LogSource, error)
	UpdateLogSource(ctx context.Context, s *LogSource) error
	DeleteLogSource(ctx context.Context, id string) error
	// UpdateLogSourceCursor advances the incremental-pull cursor and
	// resets consecutive_failures; SetLogSourceFailures records a
	// failed pull count (0 resets).
	UpdateLogSourceCursor(ctx context.Context, id string, ts time.Time, hash string) error
	SetLogSourceFailures(ctx context.Context, id string, n int) error

	CreateMonitoringChannel(ctx context.Context, c *MonitoringChannel) error
	GetMonitoringChannel(ctx context.Context, id string) (*MonitoringChannel, error)
	ListMonitoringChannels(ctx context.Context, workspaceID string) ([]*MonitoringChannel, error)
	UpdateMonitoringChannel(ctx context.Context, c *MonitoringChannel) error
	DeleteMonitoringChannel(ctx context.Context, id string) error

	// Distiller surface (M3). UpsertLogTemplate returns isNew — the
	// novelty signal. Window counts come from CountLinesByTemplate so
	// digests are stateless.
	UpsertLogTemplate(ctx context.Context, t *LogTemplate, n int64) (bool, error)
	GetLogTemplate(ctx context.Context, id string) (*LogTemplate, error)
	ListLogTemplates(ctx context.Context, sourceIDs []string, since time.Time, limit int) ([]*LogTemplate, error)
	AckLogTemplate(ctx context.Context, id, note string) error
	InsertLogLines(ctx context.Context, lines []LogLine) error
	PruneLogLines(ctx context.Context, sourceID string, maxAge time.Time, maxBytes int64) (int64, error)
	CountLinesByTemplate(ctx context.Context, sourceIDs []string, since time.Time) (map[string]int64, error)
	SearchLogLines(ctx context.Context, sourceID, q string, limit int) ([]*LogLine, error)
	ListLogLinesByTemplate(ctx context.Context, templateID string, limit int) ([]*LogLine, error)
}
