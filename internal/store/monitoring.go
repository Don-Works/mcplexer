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
// is interpolated into a fixed remote-shell command template only after
// strict validation and quoting; SSH does not provide a true argv exec.
// CursorTS tracks the incremental pull boundary. CursorHash is an opaque,
// versioned continuity/runtime checkpoint; a continuity mismatch is evidence
// of a gap or non-monotonic stream, never proof of a restart.
// ConsecutiveFailures drives health surfacing + source-dark observations.
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
// Delivery health (migration 148) is carried on the channel itself rather than
// in a side table: "is this route working" is a property of the route, and the
// dispatcher held it only in memory until a gchat webhook died for six days
// without a single surface able to say so. See monitoring_channel_health.go for
// the derived state and the JSON contract.
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

	// ConsecutiveFailures is the current unbroken run of failed deliveries;
	// a success resets it to 0. Written only by the dispatcher.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// FirstFailureAt starts the CURRENT run (nil when not failing), so
	// "broken since" is answerable; LastFailureAt closes the bound.
	FirstFailureAt *time.Time `json:"first_failure_at,omitempty"`
	LastFailureAt  *time.Time `json:"last_failure_at,omitempty"`
	// LastError is a short, redacted reason. Never carries credentials:
	// the store scrubs on write (RedactChannelError).
	LastError string `json:"last_error,omitempty"`
	// LastSuccessAt is the last delivery this channel actually accepted.
	// nil means never — deliberately distinct from healthy.
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`

	// TargetedSinceSuccess counts notifications this channel was eligible for
	// since it last delivered. Recorded BEFORE the throttle, so unlike
	// ConsecutiveFailures it advances even while suppression prevents the
	// route being attempted — which is why HealthState derives from it.
	// Migration 149.
	TargetedSinceSuccess int `json:"targeted_since_success"`
	// LastTargetedAt is when this channel was last owed a notification.
	LastTargetedAt *time.Time `json:"last_targeted_at,omitempty"`
}

// LogTemplate is one masked line shape per source: the distiller's
// dedup unit. Count is lifetime; WindowCount resets per digest window.
// Acked templates are excluded from novelty wake-ups.
type LogTemplate struct {
	ID              string     `json:"id"` // sha256(source_id, masked shape)
	SourceID        string     `json:"source_id"`
	Masked          string     `json:"masked"`
	Severity        string     `json:"severity"`
	Count           int64      `json:"count"`
	WindowCount     int64      `json:"window_count"`
	FirstSeen       time.Time  `json:"first_seen"`
	LastSeen        time.Time  `json:"last_seen"`
	SampleFirst     string     `json:"sample_first"`
	SampleLast      string     `json:"sample_last"`
	Acked           bool       `json:"acked"`
	AckNote         string     `json:"ack_note,omitempty"`
	TriagedAt       *time.Time `json:"triaged_at,omitempty"`
	TriagedSeverity string     `json:"triaged_severity,omitempty"`
}

const (
	MonitoringDispositionActionable  = "actionable"
	MonitoringDispositionUncertain   = "uncertain"
	MonitoringDispositionEvidenceGap = "evidence-gap"
	MonitoringDispositionBenign      = "benign"
)

// ValidMonitoringDisposition reports whether d is one of the four durable
// triage outcomes understood by the Monitoring worker and store.
func ValidMonitoringDisposition(d string) bool {
	switch d {
	case MonitoringDispositionActionable, MonitoringDispositionUncertain,
		MonitoringDispositionEvidenceGap, MonitoringDispositionBenign:
		return true
	default:
		return false
	}
}

// MonitoringIncident is the stable class behind one canonical task. Exact log
// templates are linked separately, so new normalisation variants can join the
// same class without rewriting historical evidence or creating another task.
type MonitoringIncident struct {
	ID                   string     `json:"id"`
	WorkspaceID          string     `json:"workspace_id"`
	ClassKey             string     `json:"class_key"`
	TaskID               string     `json:"task_id"`
	Disposition          string     `json:"disposition"`
	Severity             string     `json:"severity"`
	Title                string     `json:"title"`
	OccurrenceCount      int64      `json:"occurrence_count"`
	EventCount           int64      `json:"event_count"`
	FirstSeen            time.Time  `json:"first_seen"`
	LastSeen             time.Time  `json:"last_seen"`
	LastNotifiedAt       *time.Time `json:"last_notified_at,omitempty"`
	LastNotifiedSeverity string     `json:"last_notified_severity,omitempty"`
	// Operator action state (migration 150). Acknowledge and silence are a
	// bounded, reversible pause of the re-notification nag; both record who and
	// when, and both carry the EFFECTIVE severity at action time so a later
	// escalation past that floor pierces the pause. AckedAt/SilencedAt nil means
	// the corresponding action is not in force. SilencedUntil is the hard expiry
	// the notification policy reads every tick — a silence can never be permanent.
	AckedAt          *time.Time `json:"acked_at,omitempty"`
	AckedBy          string     `json:"acked_by,omitempty"`
	AckedSeverity    string     `json:"acked_severity,omitempty"`
	SilencedAt       *time.Time `json:"silenced_at,omitempty"`
	SilencedUntil    *time.Time `json:"silenced_until,omitempty"`
	SilencedBy       string     `json:"silenced_by,omitempty"`
	SilencedSeverity string     `json:"silenced_severity,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// MonitoringOccurrence is one bounded episode of an incident. Episodes use a
// deterministic 15-minute bucket key, making retries idempotent while later
// collector observations update counts without another model invocation.
type MonitoringOccurrence struct {
	ID              string    `json:"id"`
	IncidentID      string    `json:"incident_id"`
	OccurrenceKey   string    `json:"occurrence_key"`
	SourceID        string    `json:"source_id,omitempty"`
	TemplateIDsJSON string    `json:"template_ids_json"`
	Severity        string    `json:"severity"`
	EventCount      int64     `json:"event_count"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
	Evidence        string    `json:"evidence,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type MonitoringTriageRecord struct {
	WorkspaceID string
	ClassKey    string
	TaskID      string
	Disposition string
	Severity    string
	Title       string
	SourceID    string
	TemplateIDs []string
	Evidence    string
	ObservedAt  time.Time
}

type MonitoringTriageResult struct {
	Incident           *MonitoringIncident   `json:"incident"`
	Occurrence         *MonitoringOccurrence `json:"occurrence"`
	NewIncident        bool                  `json:"new_incident"`
	NewOccurrence      bool                  `json:"new_occurrence"`
	ShouldNotify       bool                  `json:"should_notify"`
	NotificationReason string                `json:"notification_reason,omitempty"`
	// EffectiveSeverity is the deterministic notification severity: the
	// classifier severity raised by sustained incident age. Dispatch and
	// record notifications with THIS value rather than the raw classifier
	// severity, or an ageing incident never crosses a channel min_severity
	// floor and the operator keeps hearing nothing.
	EffectiveSeverity string `json:"effective_severity,omitempty"`
}

type MonitoringTriageCompletion struct {
	WorkspaceID string
	IncidentID  string
	TemplateIDs []string
	Disposition string
	Note        string
	RunID       string
	CompletedAt time.Time
}

// LogTemplateHistory separates retained raw-line evidence from durable
// observed-day recurrence. LogTemplate's Count/FirstSeen remain lifetime
// values even after raw retention pruning; Retained* states exactly what the
// currently retained slice can prove, while Observed* survives future pruning.
type LogTemplateHistory struct {
	RetainedCount          int64         `json:"retained_count"`
	RetainedDistinctDays   int           `json:"retained_distinct_days"`
	RetainedFirstSeen      time.Time     `json:"retained_first_seen,omitempty"`
	RetainedLastSeen       time.Time     `json:"retained_last_seen,omitempty"`
	AverageRetainedLineGap time.Duration `json:"average_retained_line_gap,omitempty"`
	// Observed* is durable across future raw-line pruning. An upgrade can
	// backfill only retained days; it never invents intervening legacy days.
	ObservedDistinctDays  int           `json:"observed_distinct_days"`
	ObservedFirstDay      time.Time     `json:"observed_first_day,omitempty"`
	ObservedLastDay       time.Time     `json:"observed_last_day,omitempty"`
	AverageObservedDayGap time.Duration `json:"average_observed_day_gap,omitempty"`
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
	switch c.Kind {
	case ChannelKindGChatWebhook:
		if value, _ := cfg["auth_scope_id"].(string); strings.TrimSpace(value) == "" {
			return &FieldError{Code: "invalid_channel_config", Field: "config_json",
				Message: "gchat_webhook requires auth_scope_id"}
		}
	case ChannelKindTelegram:
		if value, _ := cfg["chat_id"].(string); strings.TrimSpace(value) == "" {
			return &FieldError{Code: "invalid_channel_config", Field: "config_json",
				Message: "telegram requires chat_id"}
		}
	case ChannelKindWhatsApp:
		if tool, _ := cfg["tool"].(string); tool != "" && tool != "openwa__send_text" {
			return &FieldError{Code: "invalid_channel_config", Field: "config_json",
				Message: "whatsapp tool must be openwa__send_text"}
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
	// Channel delivery health lives on MonitoringChannelHealthStore, a
	// consumer-boundary interface — see monitoring_channel_health.go.

	// Distiller surface (M3). UpsertLogTemplate returns isNew — the
	// novelty signal. Window counts come from CountLinesByTemplate so
	// digests are stateless.
	UpsertLogTemplate(ctx context.Context, t *LogTemplate, n int64) (bool, error)
	GetLogTemplate(ctx context.Context, id string) (*LogTemplate, error)
	ListLogTemplates(ctx context.Context, sourceIDs []string, since time.Time, limit int) ([]*LogTemplate, error)
	// ListPendingLogTemplates returns unacknowledged templates that have not
	// completed durable triage. Unlike a rolling-window query, pending rows
	// remain visible until committed, so a digest token budget cannot starve
	// lower-ranked entries forever.
	ListPendingLogTemplates(ctx context.Context, sourceIDs []string, limit int) ([]*LogTemplate, error)
	AckLogTemplate(ctx context.Context, id, note string) error
	InsertLogLines(ctx context.Context, lines []LogLine) error
	PruneLogLines(ctx context.Context, sourceID string, maxAge time.Time, maxBytes int64) (int64, error)
	CountLinesByTemplate(ctx context.Context, sourceIDs []string, since time.Time) (map[string]int64, error)
	SearchLogLines(ctx context.Context, sourceID, q string, limit int) ([]*LogLine, error)
	ListLogLinesByTemplate(ctx context.Context, templateID string, limit int) ([]*LogLine, error)

	// CountErrorLinesInWindows supports deterministic rate-spike detection
	// with current and trailing-baseline counts from one indexed scan.
	CountErrorLinesInWindows(ctx context.Context, sourceID string, baselineSince, currentSince time.Time) (current int64, baseline int64, err error)
	GetLogSourceErrorSpikeActive(ctx context.Context, sourceID string) (bool, error)
	SetLogSourceErrorSpikeActive(ctx context.Context, sourceID string, active bool) error

	GetMonitoringIncidentByClass(ctx context.Context, workspaceID, classKey string) (*MonitoringIncident, error)
	ListMonitoringIncidentsByTemplateIDs(ctx context.Context, workspaceID string, templateIDs []string) ([]*MonitoringIncident, error)
	RecordMonitoringTriage(ctx context.Context, in MonitoringTriageRecord) (*MonitoringTriageResult, error)
	ClaimMonitoringTriageTemplates(ctx context.Context, workspaceID, runID string, templateIDs []string, at time.Time) error
	CompleteMonitoringTriage(ctx context.Context, in MonitoringTriageCompletion) error
	MarkMonitoringIncidentNotified(ctx context.Context, incidentID, severity string, at time.Time) error
	HasMonitoringTriageReceipt(ctx context.Context, workspaceID, runID string) (bool, error)
}
