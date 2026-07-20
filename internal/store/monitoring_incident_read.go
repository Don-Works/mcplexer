// monitoring_incident_read.go — the operator read model for incidents.
//
// monitoring_incidents and monitoring_occurrences were written by the triage
// and absence paths and read by nothing outside the daemon: there was no way
// to ask "what is currently wrong?" without the dashboard. This file is the
// query side of that, and it carries the two derived facts a raw row cannot
// answer on its own.
//
// EffectiveSeverity is the first. The stored `severity` column is the
// classifier's verdict; the severity that actually governs notification is that
// value raised by sustained age (see the persistence policy in sqlite). An
// operator comparing a stored `warn` against a channel floor of `error` and
// concluding "this will never page me" would be wrong, so the effective value
// is served alongside rather than left to be re-derived.
//
// ClassKind is the second. Class keys are namespaced strings, and the ONE
// distinction that changes what an operator does — a signal that genuinely
// stopped versus collection that broke and cannot see it — is encoded only in
// that prefix. Forcing every client to string-match "absence-collection:" makes
// the wire format load-bearing; ClassifyIncidentClassKey makes it a field.
package store

import (
	"context"
	"strings"
	"time"
)

// Incident class kinds. These are the machine-readable form of the class-key
// namespaces: "template:"/"correlation:" from the triage worker,
// "absence:"/"absence-collection:" from the expected-signal evaluator.
//
// IncidentClassAbsence and IncidentClassCollection are deliberately separate
// values and must stay that way. "the orders stopped" and "we lost sight of the
// orders" are different incidents with different fixes; collapsing them is how
// an operator spends an hour on a broken integration that was really a broken
// SSH key, or shrugs off a real outage as a collector blip.
const (
	IncidentClassTemplate    = "template"
	IncidentClassCorrelation = "correlation"
	IncidentClassAbsence     = "absence"
	IncidentClassCollection  = "collection"
	IncidentClassOther       = "other"
)

// classKeyPrefixes maps each namespace onto its kind. Order matters at match
// time: "absence-collection:" must be tested before "absence:" would ever be
// considered, which the explicit slice (rather than a map) guarantees.
var classKeyPrefixes = []struct {
	prefix string
	kind   string
}{
	{"absence-collection:", IncidentClassCollection},
	{"absence:", IncidentClassAbsence},
	{"template:", IncidentClassTemplate},
	{"correlation:", IncidentClassCorrelation},
}

// ClassifyIncidentClassKey splits a class key into its machine-readable kind
// and the id it references (an expected-signal rule id for the absence kinds, a
// template id for "template:", a correlation hash otherwise). An unrecognised
// key yields IncidentClassOther and an empty ref rather than an error: class
// keys are an open vocabulary and a future namespace must not break this read
// path.
func ClassifyIncidentClassKey(classKey string) (kind, ref string) {
	for _, p := range classKeyPrefixes {
		if strings.HasPrefix(classKey, p.prefix) {
			return p.kind, strings.TrimPrefix(classKey, p.prefix)
		}
	}
	return IncidentClassOther, ""
}

// MonitoringIncidentView is one incident plus the facts derived from the
// notification policy. The embedded pointer flattens into the same JSON shape
// the incident already has, so the derived fields are additive.
type MonitoringIncidentView struct {
	*MonitoringIncident
	// EffectiveSeverity is Severity raised by sustained age — the value the
	// dispatcher compares against channel min_severity floors.
	EffectiveSeverity string `json:"effective_severity"`
	// ClassKind is one of the IncidentClass* constants.
	ClassKind string `json:"class_kind"`
	// ClassRef is the id the class key points at, when it has one.
	ClassRef string `json:"class_ref,omitempty"`
	// ExpectedSignalID is set for the absence and collection kinds only: the
	// rule whose evaluation raised this incident, so a client can follow
	// straight to /monitoring/expected-signals without parsing anything.
	ExpectedSignalID string `json:"expected_signal_id,omitempty"`
	// Active reports whether the incident is still being observed (LastSeen
	// inside the policy's active window). Resolution is represented by
	// LastSeen going stale, so this is the "is it still happening" answer.
	Active bool `json:"active"`
}

// Incident status filter values for MonitoringIncidentFilter.Status.
const (
	MonitoringIncidentStatusActive   = "active"
	MonitoringIncidentStatusInactive = "inactive"
)

// MonitoringIncidentFilter bounds an incident list query. WorkspaceID is
// required by every implementation — an unscoped incident list would let one
// workspace read another's operational state.
type MonitoringIncidentFilter struct {
	WorkspaceID string
	// Disposition filters on the durable triage outcome; empty means all.
	Disposition string
	// Status is "", MonitoringIncidentStatusActive or
	// MonitoringIncidentStatusInactive.
	Status string
	// Since filters on LastSeen; zero means no lower bound.
	Since time.Time
	// Limit bounds the result. Zero or negative means the default.
	Limit int
}

// Incident list bounds. The default keeps a UI page honest; the max stops a
// caller from asking for the whole table.
const (
	MonitoringIncidentListDefaultLimit = 100
	MonitoringIncidentListMaxLimit     = 500
	// MonitoringOccurrenceListMaxLimit bounds the episode ledger served with
	// one incident.
	MonitoringOccurrenceListMaxLimit = 200
)

// MonitoringIncidentReadStore is the operator read surface. It is a separate
// interface from MonitoringStore for the same reason
// MonitoringExpectedSignalStore is: folding three read methods into the big
// interface would force every store mock in the tree to grow them.
type MonitoringIncidentReadStore interface {
	// ListMonitoringIncidents returns one workspace's incidents, most
	// recently seen first.
	ListMonitoringIncidents(ctx context.Context, f MonitoringIncidentFilter) ([]*MonitoringIncidentView, error)
	// GetMonitoringIncident returns one incident scoped to a workspace. A row
	// belonging to another workspace is reported as ErrMonitoringIncidentNotFound,
	// so the error shape cannot be used to enumerate foreign ids.
	GetMonitoringIncident(ctx context.Context, workspaceID, id string) (*MonitoringIncidentView, error)
	// ListMonitoringOccurrences returns an incident's episode ledger, most
	// recent first.
	ListMonitoringOccurrences(ctx context.Context, incidentID string, limit int) ([]*MonitoringOccurrence, error)
}
