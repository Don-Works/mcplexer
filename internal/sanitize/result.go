package sanitize

// Action is the chosen disposition after scanning a tool result.
type Action string

const (
	ActionPassThrough Action = "pass_through" // no hits
	ActionEnveloped   Action = "enveloped"    // wrapped, body preserved
	ActionRedacted    Action = "redacted"     // hits replaced inline
	ActionBlocked     Action = "blocked"      // returned to caller as error
	ActionQuarantined Action = "quarantined"  // suppressed + filed for human review
)

// Result is the outcome of a sanitize pass. Body is what should be
// returned to the upstream caller (already enveloped/redacted as
// directed). Matches lists the denylist hits found, regardless of the
// chosen action.
type Result struct {
	Action  Action  `json:"action"`
	Body    string  `json:"body"`
	Matches []Match `json:"matches,omitempty"`
	Source  string  `json:"source,omitempty"`
	Trust   string  `json:"trust,omitempty"`
}

// Audit event names used by the Guards audit pipeline. Stable strings —
// add new ones rather than renaming.
const (
	EventInjectionDetected = "sanitize.injection.detected"
	EventInjectionRedacted = "sanitize.injection.redacted"
	EventInjectionBlocked  = "sanitize.injection.blocked"
)
