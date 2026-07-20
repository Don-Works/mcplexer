// monitoring_feedback.go — closes the loop between task resolution and the
// monitoring layer.
//
// The problem this solves: a logwatch task was filed by
// monitoring__commit_triage and then, however it was resolved, monitoring
// learned nothing. The same template kept waking the model, kept filing, kept
// notifying. Both links needed to fix that already existed
// (monitoring_incidents.task_id and tasks.meta.$.logwatch_class) — nothing
// read them on closure. This is the reader.
//
// Cost contract: this path is pure bookkeeping. It runs one indexed lookup on
// terminal entry and does nothing at all for the overwhelming majority of
// tasks, which are not linked to an incident. It never invokes a model; its
// entire purpose is to stop one being woken.
package tasks

import (
	"context"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/taskstatus"
)

// monitoringOutcomeForKind maps a task's terminal vocabulary kind onto the
// monitoring resolution vocabulary. This is the whole judgement, and it is
// deliberately a two-line total function over the EXISTING kind buckets rather
// than a new parallel vocabulary:
//
//	kind "cancelled" — wontfix / rejected / abandoned / won't-fix / canceled
//	                   → benign. The operator judged this not a problem, so
//	                     suppress it: mute notifications and stop the novelty
//	                     wake-ups. This is the case that saves model spend.
//
//	kind "done"      — fixed / resolved / completed / shipped / closed
//	                   → fixed. Something real was repaired. Suppress NOTHING:
//	                     if this class comes back, that is genuine news and it
//	                     must notify exactly as it would have before.
//
// Any non-terminal kind returns "", and the caller does nothing.
func monitoringOutcomeForKind(kind string) string {
	switch kind {
	case taskstatus.KindCancelled:
		return store.MonitoringOutcomeBenign
	case taskstatus.KindDone:
		return store.MonitoringOutcomeFixed
	default:
		return ""
	}
}

// onTaskTerminal feeds a task that has just ENTERED a terminal status back to
// its monitoring incident.
//
// Best-effort by contract: a failure here must never fail the task mutation.
// Refusing to close a task because a monitoring side-effect errored would be a
// far worse outcome than a missed suppression, and the operator can always
// re-drive the feedback by reopening and re-closing.
func (s *Service) onTaskTerminal(ctx context.Context, t *store.Task, p UpdatePatch) {
	if s == nil || t == nil || s.monitoringResolutions == nil {
		return
	}
	if !taskLooksLikeMonitoringIncident(t) {
		return
	}
	outcome := monitoringOutcomeForKind(s.workspaceStatusKinds(ctx, t.WorkspaceID)[t.Status])
	if outcome == "" {
		// Terminal by explicit flag on a status with no terminal kind. Treat
		// it as "fixed": the conservative choice, because "fixed" suppresses
		// nothing. Guessing "benign" here would mute alerts on the strength of
		// an unclassified status word.
		outcome = store.MonitoringOutcomeFixed
	}
	_, _ = s.monitoringResolutions.ApplyMonitoringTaskResolution(ctx, store.MonitoringResolutionInput{
		WorkspaceID: t.WorkspaceID, TaskID: t.ID, Outcome: outcome,
		StatusText: t.Status, BySession: p.UpdatedBySessionID,
		ByActor: p.ActorKind, ResolvedAt: time.Now().UTC(),
	})
}

// onTaskReopened reverses whatever the previous resolution did.
//
// This is the case that must NOT be suppressed. Reopening a logwatch task is
// an explicit statement that the class is live again, so the suppression is
// lifted: the prior disposition is restored, the templates this resolution
// acked are un-acked and re-queued, and the incident's last_notified_at is
// cleared so the next observation is guaranteed to notify rather than being
// swallowed by the re-notify backoff.
func (s *Service) onTaskReopened(ctx context.Context, t *store.Task, p UpdatePatch) {
	if s == nil || t == nil || s.monitoringResolutions == nil {
		return
	}
	if !taskLooksLikeMonitoringIncident(t) {
		return
	}
	_, _ = s.monitoringResolutions.ClearMonitoringResolutionForTask(
		ctx, t.WorkspaceID, t.ID, store.MonitoringClearReasonReopened, p.UpdatedBySessionID)
}

// taskLooksLikeMonitoringIncident is the cheap in-memory pre-filter that keeps
// this path off the hot loop for ordinary tasks. Only rows carrying the
// logwatch class marker written by monitoring__commit_triage reach the store.
//
// A task without the marker is not an error — legacy logwatch tasks filed
// before migration 143 and every hand-written task land here and are simply
// skipped, which is why the store call is also a no-op on an unlinked task.
func taskLooksLikeMonitoringIncident(t *store.Task) bool {
	if t == nil {
		return false
	}
	value, _ := MetaGetScalar(t.Meta, "logwatch_class")
	return strings.TrimSpace(value) != ""
}

// SetMonitoringResolutionStore installs the monitoring feedback sink. Nil (the
// default on a daemon with monitoring disabled) leaves task closure behaving
// exactly as it did before.
func (s *Service) SetMonitoringResolutionStore(m store.MonitoringResolutionStore) {
	s.monitoringResolutions = m
}
