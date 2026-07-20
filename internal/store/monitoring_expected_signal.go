// monitoring_expected_signal.go — domain model for expected-signal (absence)
// detection, migration 145.
//
// Every other Monitoring detector is driven by log lines ARRIVING. An
// expected-signal rule inverts that polarity: "source S is expected to produce
// at least MinCount lines matching MatchSubstring within Window; failing that
// is an incident of Severity". Evaluation is a pure function over facts the
// daemon already holds (see EvaluateExpectedSignal) — no model is consulted,
// so absence detection adds zero token load to the log-watch worker.
package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ErrMonitoringExpectedSignalNotFound wraps ErrNotFound so callers can match
// either the specific sentinel or the generic one. It lives here rather than in
// errors.go only to keep migration 145 self-contained; move it to errors.go
// alongside the other Monitoring sentinels when convenient.
var ErrMonitoringExpectedSignalNotFound = fmt.Errorf("%w: monitoring expected signal", ErrNotFound)

// Bounds. MaxExpectedSignalWindow caps the lookback at 7 days because the
// active-period walk-back is bounded at 8 days (see activePeriodStart) and
// because raw log_lines retention defaults to 7 days — a longer window would
// silently measure a pruned range and manufacture an absence.
const (
	MaxExpectedSignalWindow  = 7 * 24 * time.Hour
	MinExpectedSignalWindow  = time.Minute
	expectedSignalDayMinutes = 24 * 60
	allWeekdaysMask          = 0x7F
)

// MonitoringExpectedSignal is one absence rule.
//
// MatchSubstring is a case-insensitive SUBSTRING, never a regex: SQLite has no
// REGEXP function registered, and operator-authored backtracking on the ingest
// path is a denial-of-service surface. Empty means "any line from this source".
// Matching runs over retained log_lines rather than log_templates on purpose —
// a template only exists once its shape has been observed at least once, which
// is exactly the state an absence rule cannot presuppose.
type MonitoringExpectedSignal struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`

	MatchSubstring string `json:"match_substring,omitempty"`
	MinSeverity    string `json:"min_severity,omitempty"`
	MinCount       int64  `json:"min_count"`
	WindowSeconds  int64  `json:"window_seconds"`
	Severity       string `json:"severity"`

	// Timezone/ActiveDaysMask/Active*Minute describe when the signal is
	// legitimately expected. Overnight quiet on a low-volume integration is
	// not an incident; a rule that fires every night at 3am gets muted and is
	// then worse than no rule at all.
	Timezone          string `json:"timezone"`
	ActiveDaysMask    int    `json:"active_days_mask"`
	ActiveStartMinute int    `json:"active_start_minute"`
	ActiveEndMinute   int    `json:"active_end_minute"`

	RequireSourceLiveness  bool `json:"require_source_liveness"`
	MaxConsecutiveFailures int  `json:"max_consecutive_failures"`
	Enabled                bool `json:"enabled"`

	LastEvaluatedAt  *time.Time `json:"last_evaluated_at,omitempty"`
	LastSignalAt     *time.Time `json:"last_signal_at,omitempty"`
	LastOutcome      string     `json:"last_outcome,omitempty"`
	LastRaisedAt     *time.Time `json:"last_raised_at,omitempty"`
	LastRecoveredAt  *time.Time `json:"last_recovered_at,omitempty"`
	ActiveIncidentID string     `json:"active_incident_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Window returns the configured lookback as a duration.
func (r *MonitoringExpectedSignal) Window() time.Duration {
	return time.Duration(r.WindowSeconds) * time.Second
}

// WindowStart is the single definition of the counting boundary. The SQL that
// counts observations and the evaluator that judges them both call it, so the
// two can never drift apart.
func (r *MonitoringExpectedSignal) WindowStart(now time.Time) time.Time {
	return now.UTC().Add(-r.Window())
}

// AbsenceClassKey and CollectionClassKey namespace incident classes away from
// the worker's "template:"/"correlation:" keys. They are per-rule and stable,
// so repeat evaluations converge on ONE incident rather than creating a new one
// every tick — and absence never merges with "we cannot see", which is a
// different incident with a different fix.
func (r *MonitoringExpectedSignal) AbsenceClassKey() string { return "absence:" + r.ID }

func (r *MonitoringExpectedSignal) CollectionClassKey() string {
	return "absence-collection:" + r.ID
}

// Location resolves Timezone, defaulting to UTC. An unloadable zone is an
// explicit error: silently falling back to UTC would shift an operator's
// business hours by up to a day and produce exactly the 3am false positive
// this design exists to avoid.
func (r *MonitoringExpectedSignal) Location() (*time.Location, error) {
	name := strings.TrimSpace(r.Timezone)
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("expected signal %q: load timezone %q: %w", r.Name, name, err)
	}
	return loc, nil
}

// ApplyExpectedSignalDefaults fills zero-valued knobs. Defaults are the safe
// end of every trade-off: always-on schedule, liveness required, and a
// collection-raise threshold matching the collector's own source-dark policy.
func ApplyExpectedSignalDefaults(r *MonitoringExpectedSignal) {
	if r.MinCount <= 0 {
		r.MinCount = 1
	}
	if r.Severity == "" {
		r.Severity = SeverityError
	}
	if strings.TrimSpace(r.Timezone) == "" {
		r.Timezone = "UTC"
	}
	if r.ActiveDaysMask == 0 {
		r.ActiveDaysMask = allWeekdaysMask
	}
	if r.ActiveStartMinute == 0 && r.ActiveEndMinute == 0 {
		r.ActiveEndMinute = expectedSignalDayMinutes
	}
	if r.MaxConsecutiveFailures <= 0 {
		r.MaxConsecutiveFailures = 3
	}
}

// ValidateMonitoringExpectedSignal enforces the rule's bounds.
func ValidateMonitoringExpectedSignal(r *MonitoringExpectedSignal) error {
	switch {
	case r == nil:
		return &FieldError{Code: "missing_rule", Field: "rule", Message: "rule is required"}
	case strings.TrimSpace(r.Name) == "":
		return &FieldError{Code: "missing_name", Field: "name", Message: "name is required"}
	case strings.TrimSpace(r.SourceID) == "":
		return &FieldError{Code: "missing_source", Field: "source_id", Message: "source_id is required"}
	case r.MinCount <= 0:
		return &FieldError{Code: "invalid_min_count", Field: "min_count",
			Message: "min_count must be >= 1"}
	case !ValidSeverity(r.Severity):
		return &FieldError{Code: "invalid_severity", Field: "severity", Value: r.Severity,
			Message: "severity must be info|warn|error|critical"}
	case r.MinSeverity != "" && !ValidSeverity(r.MinSeverity):
		return &FieldError{Code: "invalid_min_severity", Field: "min_severity", Value: r.MinSeverity,
			Message: "min_severity must be empty or info|warn|error|critical"}
	}
	if err := validateExpectedSignalWindow(r); err != nil {
		return err
	}
	return validateExpectedSignalSchedule(r)
}

func validateExpectedSignalWindow(r *MonitoringExpectedSignal) error {
	window := r.Window()
	if window < MinExpectedSignalWindow || window > MaxExpectedSignalWindow {
		return &FieldError{Code: "invalid_window", Field: "window_seconds",
			Value:   fmt.Sprintf("%d", r.WindowSeconds),
			Message: "window_seconds must be between 60 and 604800 (7 days)",
			Hint:    "raw log lines are pruned by source retention; a longer window would measure a pruned range",
		}
	}
	if len(r.MatchSubstring) > 500 {
		return &FieldError{Code: "invalid_match", Field: "match_substring",
			Message: "match_substring is capped at 500 characters"}
	}
	return nil
}

func validateExpectedSignalSchedule(r *MonitoringExpectedSignal) error {
	if r.ActiveDaysMask <= 0 || r.ActiveDaysMask > allWeekdaysMask {
		return &FieldError{Code: "invalid_active_days", Field: "active_days_mask",
			Message: "active_days_mask must be a 1-127 bitmask (bit 0 = Sunday)"}
	}
	for field, minute := range map[string]int{
		"active_start_minute": r.ActiveStartMinute,
		"active_end_minute":   r.ActiveEndMinute,
	} {
		if minute < 0 || minute > expectedSignalDayMinutes {
			return &FieldError{Code: "invalid_active_window", Field: field,
				Message: "active minutes must be within 0-1440"}
		}
	}
	if _, err := r.Location(); err != nil {
		return &FieldError{Code: "invalid_timezone", Field: "timezone", Value: r.Timezone,
			Message: "timezone must be a loadable IANA zone", Cause: err}
	}
	return nil
}

// ExpectedSignalObservation is what the daemon measured over the rule window.
// TotalLines counts EVERY retained line from the source regardless of pattern:
// it is the collection-liveness fact that separates "no orders" from "we lost
// the stream". LastMatchAt spans all retained history, not just the window.
type ExpectedSignalObservation struct {
	MatchCount  int64      `json:"match_count"`
	TotalLines  int64      `json:"total_lines"`
	LastMatchAt *time.Time `json:"last_match_at,omitempty"`
}

// SourceCollectionHealth is the pull-side truth. CursorTS is deliberately
// absent: it is a log WATERMARK advanced from line timestamps, so a stalled
// cursor proves nothing about pull health and a fresh cursor does not prove
// the pull is working.
type SourceCollectionHealth struct {
	Enabled             bool `json:"enabled"`
	ConsecutiveFailures int  `json:"consecutive_failures"`
}

// ExpectedSignalRecord is one evaluation outcome heading for durable storage.
// TaskID is required only when Decision.Raise is true — the caller owns
// canonical-task election (tasks.Service), exactly as the triage handler does.
type ExpectedSignalRecord struct {
	RuleID     string
	TaskID     string
	Decision   ExpectedSignalDecision
	ObservedAt time.Time
}

// ExpectedSignalResult reports what the store did. Incident/Occurrence are nil
// when the decision did not raise. Recovered is true on the evaluation that
// clears a previously active incident — the caller closes the canonical task.
type ExpectedSignalResult struct {
	Rule               *MonitoringExpectedSignal `json:"rule"`
	Incident           *MonitoringIncident       `json:"incident,omitempty"`
	Occurrence         *MonitoringOccurrence     `json:"occurrence,omitempty"`
	NewIncident        bool                      `json:"new_incident"`
	NewOccurrence      bool                      `json:"new_occurrence"`
	Recovered          bool                      `json:"recovered"`
	ShouldNotify       bool                      `json:"should_notify"`
	NotificationReason string                    `json:"notification_reason,omitempty"`
	// EffectiveSeverity is the incident severity after the shared
	// persistence/age-escalation policy. Dispatch and MarkMonitoringIncidentNotified
	// with this, not Decision.Severity.
	EffectiveSeverity string `json:"effective_severity,omitempty"`
}

// MonitoringExpectedSignalStore is defined at the consumer boundary rather than
// folded into MonitoringStore so adding absence detection does not force every
// existing store mock across the tree to grow eight methods. *sqlite.DB
// satisfies it; promote it into store.Store when the mocks are updated.
type MonitoringExpectedSignalStore interface {
	CreateMonitoringExpectedSignal(ctx context.Context, r *MonitoringExpectedSignal) error
	GetMonitoringExpectedSignal(ctx context.Context, id string) (*MonitoringExpectedSignal, error)
	ListMonitoringExpectedSignals(ctx context.Context, workspaceID string) ([]*MonitoringExpectedSignal, error)
	// ListEnabledMonitoringExpectedSignals spans all workspaces — the
	// evaluator tick's scheduling view.
	ListEnabledMonitoringExpectedSignals(ctx context.Context) ([]*MonitoringExpectedSignal, error)
	UpdateMonitoringExpectedSignal(ctx context.Context, r *MonitoringExpectedSignal) error
	DeleteMonitoringExpectedSignal(ctx context.Context, id string) error

	// ObserveExpectedSignal gathers the evaluator's inputs in one indexed
	// scan plus one source read. It performs no judgement.
	ObserveExpectedSignal(ctx context.Context, r *MonitoringExpectedSignal, now time.Time) (
		ExpectedSignalObservation, SourceCollectionHealth, error)
	// RecordExpectedSignalOutcome persists evaluation state and, when the
	// decision raises, converges on the incident class through the existing
	// monitoring_incidents / monitoring_occurrences machinery.
	RecordExpectedSignalOutcome(ctx context.Context, in ExpectedSignalRecord) (*ExpectedSignalResult, error)
}
