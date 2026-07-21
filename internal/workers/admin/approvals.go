package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
	"github.com/oklog/ulid/v2"
)

// ApprovalsStore is the slice of store.Store this surface needs.
// Re-stated here so the admin package has no direct sqlite import
// dependency beyond the Store interface.
type ApprovalsStore interface {
	CreateWorkerApproval(ctx context.Context, a *store.WorkerApproval) error
	GetWorkerApproval(ctx context.Context, id string) (*store.WorkerApproval, error)
	ListWorkerApprovals(ctx context.Context, status string, limit int) ([]*store.WorkerApproval, error)
	DecideWorkerApproval(ctx context.Context, id, decision, decidedBy, resumedRunID string, decidedAt time.Time) error
}

// ListApprovalsInput mirrors GET /api/v1/worker-approvals.
type ListApprovalsInput struct {
	Status string `json:"status,omitempty"` // "" = all
	Limit  int    `json:"limit,omitempty"`
}

// ListApprovals returns approvals filtered by status (empty matches
// every status). Ordered created_at DESC.
func (s *Service) ListApprovals(
	ctx context.Context, in ListApprovalsInput,
) ([]*store.WorkerApproval, error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	rows, err := s.store.ListWorkerApprovals(ctx, in.Status, in.Limit)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	return rows, nil
}

// GetApproval returns one approval row by id.
func (s *Service) GetApproval(
	ctx context.Context, id string,
) (*store.WorkerApproval, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("id required")
	}
	return s.store.GetWorkerApproval(ctx, id)
}

// ApproveOutput is returned by ApproveAndResume.
type ApproveOutput struct {
	ApprovalID    string `json:"approval_id"`
	Status        string `json:"status"`         // approved
	ResumedRunID  string `json:"resumed_run_id"` // new run id, empty when no runner wired
	OriginalRunID string `json:"original_run_id"`
}

// ApproveAndResume marks the approval as approved AND fires a NEW run
// with PreApprovedTools = []string{toolName} so propose-mode gating
// skips this single tool. The "resume" is best-effort: we cannot
// resume the original loop from the point it was stopped (that would
// need loop-state snapshotting — see runner package comments).
// Spawning a new run is acceptable for M1; the worker prompt + skill
// stays the same so the model gets another shot at the same task with
// the write tool pre-cleared. Returns the new run id.
func (s *Service) ApproveAndResume(
	ctx context.Context, id, decidedBy string,
) (*ApproveOutput, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("id required")
	}
	row, err := s.store.GetWorkerApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Status != "pending" {
		return nil, fmt.Errorf("approval %q already decided (%s)", id, row.Status)
	}
	out := &ApproveOutput{
		ApprovalID:    row.ID,
		Status:        "approved",
		OriginalRunID: row.RunID,
	}
	if s.runner != nil {
		runID, runErr := s.runner.RunWithOpts(ctx, row.WorkerID, runner.RunOpts{
			PreApprovedTools: []string{row.ToolName},
		})
		if runErr != nil {
			return nil, fmt.Errorf("resume run: %w", runErr)
		}
		out.ResumedRunID = runID
	}
	// Persist the decision AFTER the resume run is in flight so we don't
	// leave a "decided=approved, resumed_run_id=''" row in a half-state
	// if the runner failed mid-spawn.
	if err := s.store.DecideWorkerApproval(
		ctx, row.ID, "approved", decidedBy, out.ResumedRunID, s.clock.Now(),
	); err != nil {
		return nil, fmt.Errorf("record decision: %w", err)
	}
	s.recordApprovalAudit(ctx, row, "approved", decidedBy, out.ResumedRunID)
	return out, nil
}

// RejectOutput is returned by Reject.
type RejectOutput struct {
	ApprovalID    string `json:"approval_id"`
	Status        string `json:"status"` // rejected
	OriginalRunID string `json:"original_run_id"`
}

// Reject marks the approval row as rejected and stamps the original
// run as Status="rejected" so the operator's intent shows in the run
// ledger. Idempotent — re-rejecting a decided row returns the sentinel.
func (s *Service) Reject(
	ctx context.Context, id, decidedBy string,
) (*RejectOutput, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("id required")
	}
	row, err := s.store.GetWorkerApproval(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Status != "pending" {
		return nil, fmt.Errorf("approval %q already decided (%s)", id, row.Status)
	}
	if err := s.store.DecideWorkerApproval(
		ctx, row.ID, "rejected", decidedBy, "", s.clock.Now(),
	); err != nil {
		return nil, fmt.Errorf("record rejection: %w", err)
	}
	// Best-effort: stamp the run row as rejected. A failure here is
	// non-fatal — the approval ledger is the source of truth for the
	// decision; the run-row stamp is a UI convenience.
	if err := s.store.UpdateWorkerRunStatus(ctx, row.RunID, store.WorkerRunFinalize{
		Status:     "rejected",
		FinishedAt: s.clock.Now(),
	}); err != nil {
		// Don't return — the decision is recorded. Logging at the HTTP
		// handler level would double-emit; we swallow here and let the
		// admin tool surface the partial outcome.
		_ = err
	}
	s.recordApprovalAudit(ctx, row, "rejected", decidedBy, "")
	return &RejectOutput{
		ApprovalID:    row.ID,
		Status:        "rejected",
		OriginalRunID: row.RunID,
	}, nil
}

// recordApprovalAudit writes a worker_approval.decided audit record so
// the audit ledger captures the operator's decision. Nil-safe on the
// auditor; failures are silently swallowed (the decision is already
// persisted in worker_approvals).
func (s *Service) recordApprovalAudit(
	ctx context.Context, row *store.WorkerApproval, decision, decidedBy, resumedRunID string,
) {
	if s.auditor == nil {
		return
	}
	payload := map[string]any{
		"worker_id":      row.WorkerID,
		"run_id":         row.RunID,
		"approval_id":    row.ID,
		"tool_name":      row.ToolName,
		"decision":       decision,
		"decided_by":     decidedBy,
		"resumed_run_id": resumedRunID,
	}
	raw, _ := json.Marshal(payload)
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      s.clock.Now().UTC(),
		ClientType:     "worker",
		SessionID:      "worker:" + row.WorkerID,
		ToolName:       "worker_approval.decided",
		ParamsRedacted: raw,
		Status:         decision,
		CreatedAt:      s.clock.Now().UTC(),
		ActorKind:      "worker",
		ActorID:        row.WorkerID,
		CorrelationID:  row.RunID,
	}
	_ = s.auditor.Record(ctx, rec)
}
