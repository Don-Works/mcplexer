package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

const (
	maxListRunsLimit      = 100
	runPromptPreviewBytes = 2048
	runOutputPreviewBytes = 4096
	runErrorPreviewBytes  = 1024
)

// RunNowOutput is the mcplexer__run_worker_now response shape.
type RunNowOutput struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// RunNow fires an ad-hoc manual run with no external trigger context.
//
// When the runner is wired (M0.3 in production), we delegate to it and
// the run row is fully materialised. When the runner is nil (early dev
// builds), we insert a status="running" placeholder row directly via
// the store so the run id can be returned to the agent immediately — see
// the TODO below; the actual exec is picked up once the runner lands.
func (s *Service) RunNow(ctx context.Context, id string) (RunNowOutput, error) {
	return s.RunNowWithOpts(ctx, id, runner.RunOpts{TriggerKind: "manual"})
}

// RunNowWithOpts is RunNow with caller-supplied RunOpts overrides.
// Today the spawn_subagent handler uses this to pass the parent
// trigger_message_id through so the sub-agent's reply_to_trigger
// output channel chains back to the original mesh message. Other
// callers (mcplexer__run_worker_now over MCP) continue using RunNow.
//
// IMPORTANT: callers that need the worker's wall-clock to exceed the
// caller's own context deadline MUST dispatch RunNowWithOpts on a
// goroutine with a detached context (see spawn_subagent and
// handleRunWorkerNow for examples). This method does NOT detach the
// context internally — it trusts the caller to provide one with an
// appropriate deadline.
func (s *Service) RunNowWithOpts(ctx context.Context, id string, opts runner.RunOpts) (RunNowOutput, error) {
	if strings.TrimSpace(id) == "" {
		return RunNowOutput{}, errors.New("id required")
	}
	w, err := s.store.GetWorker(ctx, id)
	if err != nil {
		s.emitAuditRunNow(ctx, id, "", "", "error", err.Error())
		return RunNowOutput{}, err
	}
	if !w.Enabled {
		err := fmt.Errorf("%w: %s", runner.ErrWorkerDisabled, w.ID)
		s.emitAuditRunNow(ctx, w.ID, "", "", "error", err.Error())
		return RunNowOutput{}, err
	}
	if s.runner != nil {
		runID, runErr := s.runner.RunWithOpts(ctx, w.ID, opts)
		if runErr != nil {
			s.emitAuditRunNow(ctx, w.ID, "", "", "error", runErr.Error())
			return RunNowOutput{}, runErr
		}
		s.emitAuditRunNow(ctx, w.ID, runID, "", "ok", "")
		return RunNowOutput{RunID: runID, Status: "running"}, nil
	}
	// TODO(M0.5/M0.3 bridge): when the daemon is built without the
	// runner wired (e.g. stdio-only mode in cmd/mcplexer/serve.go's
	// runStdio path), write a placeholder run row so the agent has a
	// trackable id. The status stays "running" forever until M0.3 takes
	// over; the buildWorkerRunner path in cmd/mcplexer always supplies
	// a real runner in the daemon proper, so this branch only ever
	// activates in stdio mode.
	run := &store.WorkerRun{
		ID:            "run-" + uuid.NewString(),
		WorkerID:      w.ID,
		WorkspaceID:   w.WorkspaceID,
		StartedAt:     s.clock.Now(),
		Status:        "running",
		ModelProvider: w.ModelProvider,
		ModelID:       w.ModelID,
		TriggerKind:   opts.TriggerKind,
	}
	if err := s.store.CreateWorkerRun(ctx, run); err != nil {
		s.emitAuditRunNow(ctx, w.ID, "", "", "error", err.Error())
		return RunNowOutput{}, fmt.Errorf("stub run: %w", err)
	}
	s.emitAuditRunNow(ctx, w.ID, run.ID, "", "ok", "")
	return RunNowOutput{RunID: run.ID, Status: "running"}, nil
}

// ListRunsInput is the mcplexer__list_worker_runs arg payload.
type ListRunsInput struct {
	WorkerID string `json:"worker_id"`
	Limit    int    `json:"limit,omitempty"`
	Status   string `json:"status,omitempty"`
}

// ListRuns returns recent runs for one worker, filtered by status. Each
// returned WorkerRun is annotated with ToolCallsCountSource ("native"
// vs "derived") so the UI can flag CLI-adapter runs whose
// tool_calls_count is derived from audit_records rather than reported
// natively by the model adapter.
func (s *Service) ListRuns(ctx context.Context, in ListRunsInput) ([]*store.WorkerRun, error) {
	if strings.TrimSpace(in.WorkerID) == "" {
		return nil, errors.New("worker_id required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > maxListRunsLimit {
		limit = maxListRunsLimit
	}
	runs, err := s.store.ListWorkerRuns(ctx, in.WorkerID, limit)
	if err != nil {
		return nil, err
	}
	if in.Status != "" {
		filtered := make([]*store.WorkerRun, 0, len(runs))
		for _, r := range runs {
			if r.Status == in.Status {
				filtered = append(filtered, r)
			}
		}
		runs = filtered
	}
	w, _ := s.store.GetWorker(ctx, in.WorkerID)
	s.annotateRunsAnnotations(ctx, runs, w)
	out := make([]*store.WorkerRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, previewRunForList(run))
	}
	return out, nil
}

func previewRunForList(run *store.WorkerRun) *store.WorkerRun {
	if run == nil {
		return nil
	}
	cp := *run
	cp.PromptRendered = truncateRunPreview(cp.PromptRendered, runPromptPreviewBytes)
	cp.OutputText = truncateRunPreview(cp.OutputText, runOutputPreviewBytes)
	cp.Error = truncateRunPreview(cp.Error, runErrorPreviewBytes)
	return &cp
}

func truncateRunPreview(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n[truncated %d bytes; use get_worker_run for full text]", len(s)-max)
}

// GetRun returns one WorkerRun by id, annotated with
// ToolCallsCountSource (and the derived tool_calls_count for CLI-family
// adapters whose ToolCalls slice is structurally empty).
func (s *Service) GetRun(ctx context.Context, runID string) (*store.WorkerRun, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("run_id required")
	}
	run, err := s.store.GetWorkerRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	w, _ := s.store.GetWorker(ctx, run.WorkerID)
	s.annotateRunAnnotations(ctx, run, w)
	return run, nil
}

// CancelRunInput is the mcplexer__cancel_worker_run arg payload.
type CancelRunInput struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

// CancelRunOutput is the mcplexer__cancel_worker_run response shape.
type CancelRunOutput struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// CancelRun hard-stops a worker run, finalising it as the distinct
// status=cancelled (NOT failure — an operator pulling the plug is not a
// worker failing). Two paths, selected by whether a LIVE runner entry
// exists for the run:
//
//  1. LIVE run — the runner is wired and r.Cancel(runID) finds a live
//     execution. The runner is then the SINGLE WRITER of the terminal
//     row: it interrupts the model↔tool loop, kills any CLI subprocess
//     group, and finalises status=cancelled itself. We do NOT direct-
//     flip the DB here, so a late natural completion racing the cancel
//     can never clobber the operator's intent (and vice-versa).
//
//  2. NO live entry — orphan/stub running rows whose runner died (daemon
//     restart, panic) or rows that are already terminal. Here we
//     direct-flip via store.CancelRun, which rejects already-terminal
//     rows with ErrRunNotCancellable so the MCP/HTTP surface returns a
//     clean 409 "already finished" distinct from a 404 "not found".
func (s *Service) CancelRun(
	ctx context.Context, in CancelRunInput,
) (CancelRunOutput, error) {
	runID := strings.TrimSpace(in.RunID)
	if runID == "" {
		return CancelRunOutput{}, errors.New("run_id required")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "cancelled by operator"
	} else {
		reason = "cancelled by operator: " + reason
	}

	// Path 1 — live hard-stop. The runner owns finalization; do NOT
	// direct-flip the DB or we'd race the single writer.
	if s.runner != nil && s.runner.Cancel(runID, reason) {
		s.emitAuditCancelRun(ctx, runID, s.lookupRunWorkerID(ctx, runID), reason, "ok", "")
		return CancelRunOutput{RunID: runID, Status: runner.StatusCancelled, Reason: reason}, nil
	}

	// Path 2 — no live entry: direct-flip an orphan/stub running row.
	if err := s.store.CancelRun(ctx, runID, s.clock.Now().UTC(), reason); err != nil {
		s.emitAuditCancelRun(ctx, runID, s.lookupRunWorkerID(ctx, runID), reason, "error", err.Error())
		return CancelRunOutput{}, err
	}
	// Publish a terminal snapshot so live UI (DelegationsPage, per-run
	// streams, Worker detail) updates promptly without waiting for the
	// next poll. The live path skips this — the runner publishes its own
	// authoritative terminal frame on finalize.
	if snap, gerr := s.store.GetWorkerRun(ctx, runID); gerr == nil && snap != nil {
		s.emitAuditCancelRun(ctx, runID, snap.WorkerID, reason, "ok", "")
		if s.runBus != nil {
			s.runBus.Publish(&runner.RunEvent{
				Kind:     runner.RunEventKindStatus,
				WorkerID: snap.WorkerID,
				RunID:    snap.ID,
				Run:      snap,
			})
		}
	} else {
		s.emitAuditCancelRun(ctx, runID, "", reason, "ok", "")
	}
	return CancelRunOutput{RunID: runID, Status: runner.StatusCancelled, Reason: reason}, nil
}

// lookupRunWorkerID returns the worker_id for runID on a best-effort
// basis so audit rows tie a cancel back to its worker. Returns "" when
// the row can't be read.
func (s *Service) lookupRunWorkerID(ctx context.Context, runID string) string {
	if run, gerr := s.store.GetWorkerRun(ctx, runID); gerr == nil && run != nil {
		return run.WorkerID
	}
	return ""
}
