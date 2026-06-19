package runner

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// RenderPromptForTest exposes the unexported renderPrompt to the
// black-box runner_test package without leaking it to other callers.
func RenderPromptForTest(template, parametersJSON string) (string, error) {
	return renderPrompt(template, parametersJSON)
}

// ResolveWorkspacePathForTest exposes the unexported resolveWorkspacePath
// to the black-box runner_test package so the dir-ensure behaviour can be
// asserted without driving a full Run.
func (r *Runner) ResolveWorkspacePathForTest(ctx context.Context, workspaceID string) string {
	return r.resolveWorkspacePath(ctx, workspaceID)
}

// ResolveAPIKeyForTest exposes the unexported resolveAPIKey so the
// black-box runner_test package can assert the gated-CLI-provider
// short-circuit and the missing-SecretReader error path directly,
// without driving a full Run.
func (r *Runner) ResolveAPIKeyForTest(ctx context.Context, worker *store.Worker) (string, error) {
	return r.resolveAPIKey(ctx, worker)
}

// MergeWorkerCapsForTest exposes the unexported mergeWorkerCaps so the
// black-box runner_test package can unit-test the per-field inherit
// semantics + the MaxOutputTokens->lifetimeOutputCap mapping in
// isolation from the e2e run path.
func MergeWorkerCapsForTest(base Caps, w *store.Worker) (Caps, int) {
	return mergeWorkerCaps(base, w)
}

// Hard-stop registry test seams. These let focused unit tests exercise
// the live-cancel control surface without standing up a full Run.
func (r *Runner) RegisterActiveRunForTest(runID string, cancel context.CancelCauseFunc) {
	r.registerActiveRun(runID, cancel)
}

func (r *Runner) UnregisterActiveRunForTest(runID string) {
	r.unregisterActiveRun(runID)
}

func (r *Runner) ActiveRunCountForTest() int {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return len(r.activeRuns)
}

// OperatorCancelReasonForTest exposes the stored cancel reason so tests
// can assert Cancel stamps the operator message before signalling.
func (r *Runner) OperatorCancelReasonForTest(runID string) string {
	return r.operatorCancelReason(runID)
}

// MapCancelCauseForTest deterministically exercises ctxCancelOutcome's
// cause→status mapping without driving a full run. kind selects which
// sentinel cause is attached to the cancelled context:
//
//	"operator"  → errOperatorCancel   → "cancelled"
//	"wallclock" → errWallClockExceeded → "cap_exceeded"
//	anything    → context.Canceled    → "failure"
func (r *Runner) MapCancelCauseForTest(kind string) string {
	base := context.Background()
	var ctx context.Context
	switch kind {
	case "operator":
		c, cancel := context.WithCancelCause(base)
		cancel(errOperatorCancel)
		ctx = c
	case "wallclock":
		c, cancel := context.WithDeadlineCause(base, time.Now().Add(-time.Second), errWallClockExceeded)
		defer cancel()
		ctx = c
	default:
		c, cancel := context.WithCancel(base)
		cancel()
		ctx = c
	}
	s := &loopState{runID: "probe", caps: Caps{MaxWallClock: time.Minute}}
	return r.ctxCancelOutcome(ctx, s).status
}
