// monitoring_incident_action.go — the operator's levers on a live incident.
//
// Three verbs, deliberately distinct, each mapped onto machinery that already
// exists rather than a parallel suppression system:
//
//   - acknowledge: "I've seen it — stop nagging me, but keep it open." Pauses
//     re-notification while the incident stays OPEN and keeps updating. It does
//     NOT survive an escalation: if the effective severity rises above the level
//     at which it was acked, the incident notifies again. Records actor + time.
//
//   - silence(duration): "be quiet until then, then tell me again if it's live."
//     A BOUNDED pause that auto-expires. After expiry a still-active incident
//     re-notifies. An unbounded duration is rejected — a silence that cannot
//     expire is a permanent mute, which is what dismiss is for. Reversible via
//     unsilence. Escalation past the floor pierces it, exactly like ack.
//     Records actor + time + until.
//
//   - dismiss: "it's over / not worth tracking." Resolves the incident through
//     the EXISTING benign/resolution vocabulary (disposition=benign plus a
//     reversible monitoring_resolutions receipt), so it is visible and undoable
//     on the same read/clear paths a benign task resolution uses. A dismiss does
//     NOT blacklist the class: a later recurrence breaks the suppression and
//     fires again, on both the template and the absence paths.
//
// Every action is deterministic bookkeeping over columns the daemon already
// writes. No model is consulted.
package store

import (
	"context"
	"time"
)

// Silence bounds. A single silence is capped so it can never be effectively
// permanent; the minimum keeps a zero-ish duration from being silently rounded
// to nothing.
const (
	MonitoringIncidentMaxSilence = 7 * 24 * time.Hour
	MonitoringIncidentMinSilence = time.Minute
)

// MonitoringIncidentActionRef addresses one incident for an action and carries
// the mandatory attribution. Session is optional context (the agent/session
// that issued the action); Actor is required.
type MonitoringIncidentActionRef struct {
	WorkspaceID string
	IncidentID  string
	Actor       string
	Session     string
	At          time.Time
}

// MonitoringIncidentSilenceInput is an acknowledge with a bounded expiry.
type MonitoringIncidentSilenceInput struct {
	MonitoringIncidentActionRef
	// Duration must be within [MonitoringIncidentMinSilence,
	// MonitoringIncidentMaxSilence]. Anything else is ErrMonitoringSilenceUnbounded.
	Duration time.Duration
}

// MonitoringIncidentDismissInput resolves an incident as over. StatusText is the
// operator's own word for it, stored verbatim on the resolution receipt.
type MonitoringIncidentDismissInput struct {
	MonitoringIncidentActionRef
	StatusText string
}

// MonitoringIncidentActionStore is the operator action surface. Additive and
// type-asserted at the router, like the incident read store, so a daemon whose
// store predates it serves 501 rather than failing to build.
type MonitoringIncidentActionStore interface {
	// AckMonitoringIncident pauses re-notification while leaving the incident
	// open. Returns ErrMonitoringActionActorRequired when the actor is empty and
	// ErrMonitoringIncidentNotFound when the incident is absent or foreign.
	AckMonitoringIncident(ctx context.Context, in MonitoringIncidentActionRef) (*MonitoringIncidentView, error)
	// UnackMonitoringIncident lifts an acknowledgement so the nag resumes.
	// Idempotent: un-acking a non-acked incident is a no-op that returns the view.
	UnackMonitoringIncident(ctx context.Context, in MonitoringIncidentActionRef) (*MonitoringIncidentView, error)
	// SilenceMonitoringIncident pauses re-notification until a bounded expiry.
	SilenceMonitoringIncident(ctx context.Context, in MonitoringIncidentSilenceInput) (*MonitoringIncidentView, error)
	// UnsilenceMonitoringIncident clears an active silence. Idempotent.
	UnsilenceMonitoringIncident(ctx context.Context, in MonitoringIncidentActionRef) (*MonitoringIncidentView, error)
	// DismissMonitoringIncident resolves the incident as over via the benign
	// resolution vocabulary. Reversible through ClearMonitoringResolution and
	// broken automatically by a recurrence.
	DismissMonitoringIncident(ctx context.Context, in MonitoringIncidentDismissInput) (*MonitoringResolution, error)
	// ListSuppressedMonitoringIncidents returns incidents whose acknowledgement
	// or silence is in force at now (view.Suppressed true), most recently seen
	// first. Dismissed incidents are surfaced through ListMonitoringResolutions
	// instead. The returned views carry AckActive/SilenceActive plus the raw
	// attribution columns, so "what is muted and why/until when" is one call.
	ListSuppressedMonitoringIncidents(ctx context.Context, workspaceID string, now time.Time, limit int) ([]*MonitoringIncidentView, error)
}
