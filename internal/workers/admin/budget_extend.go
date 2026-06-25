package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// DelegationBudgetInput increases runtime caps for live delegation runs.
// Absolute max_* fields set a higher explicit cap; additional_* fields add
// to the current explicit cap. Supplying both forms for one axis is rejected.
type DelegationBudgetInput struct {
	DelegationID string `json:"delegation_id,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`

	MaxToolCalls               *int `json:"max_tool_calls,omitempty"`
	AdditionalToolCalls        int  `json:"additional_tool_calls,omitempty"`
	MaxWallClockSeconds        *int `json:"max_wall_clock_seconds,omitempty"`
	AdditionalWallClockSeconds int  `json:"additional_wall_clock_seconds,omitempty"`
	MaxInputTokens             *int `json:"max_input_tokens,omitempty"`
	AdditionalInputTokens      int  `json:"additional_input_tokens,omitempty"`
	MaxOutputTokens            *int `json:"max_output_tokens,omitempty"`
	AdditionalOutputTokens     int  `json:"additional_output_tokens,omitempty"`

	Reason string `json:"reason,omitempty"`
}

type DelegationBudgetOutput struct {
	DelegationID string            `json:"delegation_id,omitempty"`
	Updated      int               `json:"updated"`
	Updates      []RunBudgetUpdate `json:"updates"`
}

type RunBudgetUpdate struct {
	RunID       string                       `json:"run_id"`
	WorkerID    string                       `json:"worker_id"`
	LiveUpdated bool                         `json:"live_updated"`
	Changes     map[string]BudgetFieldChange `json:"changes"`
}

type BudgetFieldChange struct {
	Old int `json:"old"`
	New int `json:"new"`
}

func (s *Service) ExtendDelegationBudget(
	ctx context.Context, in DelegationBudgetInput,
) (DelegationBudgetOutput, error) {
	if err := validateBudgetInput(in); err != nil {
		return DelegationBudgetOutput{}, err
	}
	if strings.TrimSpace(in.RunID) != "" {
		upd, err := s.extendRunBudget(ctx, strings.TrimSpace(in.RunID), in)
		if err != nil {
			return DelegationBudgetOutput{}, err
		}
		return DelegationBudgetOutput{DelegationID: in.DelegationID, Updated: 1, Updates: []RunBudgetUpdate{upd}}, nil
	}
	d, err := s.findDelegation(ctx, in.WorkspaceID, in.DelegationID)
	if err != nil {
		return DelegationBudgetOutput{}, err
	}
	out := DelegationBudgetOutput{DelegationID: d.ID}
	seenRuns := map[string]struct{}{}
	for _, wc := range d.Workers {
		if wc.LatestRun == nil || wc.LatestRun.Status != runner.StatusRunning {
			continue
		}
		if _, ok := seenRuns[wc.LatestRun.ID]; ok {
			continue
		}
		seenRuns[wc.LatestRun.ID] = struct{}{}
		upd, err := s.extendRunBudget(ctx, wc.LatestRun.ID, in)
		if err != nil {
			return DelegationBudgetOutput{}, err
		}
		out.Updates = append(out.Updates, upd)
	}
	out.Updated = len(out.Updates)
	if out.Updated == 0 {
		return DelegationBudgetOutput{}, errors.New("delegation has no running worker runs to extend")
	}
	return out, nil
}

func validateBudgetInput(in DelegationBudgetInput) error {
	if strings.TrimSpace(in.DelegationID) == "" && strings.TrimSpace(in.RunID) == "" {
		return errors.New("delegation_id or run_id required")
	}
	if strings.TrimSpace(in.DelegationID) != "" && strings.TrimSpace(in.RunID) != "" {
		return errors.New("provide delegation_id or run_id, not both")
	}
	if !budgetInputHasChange(in) {
		return errors.New("at least one budget increase is required")
	}
	return nil
}

func budgetInputHasChange(in DelegationBudgetInput) bool {
	return in.MaxToolCalls != nil || in.AdditionalToolCalls != 0 ||
		in.MaxWallClockSeconds != nil || in.AdditionalWallClockSeconds != 0 ||
		in.MaxInputTokens != nil || in.AdditionalInputTokens != 0 ||
		in.MaxOutputTokens != nil || in.AdditionalOutputTokens != 0
}

func (s *Service) findDelegation(
	ctx context.Context, workspaceID, delegationID string,
) (*DelegationContext, error) {
	id := strings.TrimSpace(delegationID)
	if id == "" {
		return nil, errors.New("delegation_id required")
	}
	rows, err := s.ListDelegations(ctx, DelegationListInput{WorkspaceID: workspaceID, Limit: 200})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Service) extendRunBudget(
	ctx context.Context, runID string, in DelegationBudgetInput,
) (RunBudgetUpdate, error) {
	run, err := s.store.GetWorkerRun(ctx, runID)
	if err != nil {
		return RunBudgetUpdate{}, err
	}
	if run.Status != runner.StatusRunning {
		return RunBudgetUpdate{}, fmt.Errorf("run %s is %s, not running", run.ID, run.Status)
	}
	w, err := s.store.GetWorker(ctx, run.WorkerID)
	if err != nil {
		return RunBudgetUpdate{}, err
	}
	if in.WorkspaceID != "" && w.WorkspaceID != in.WorkspaceID {
		return RunBudgetUpdate{}, fmt.Errorf("run %s is in workspace %s, not %s", run.ID, w.WorkspaceID, in.WorkspaceID)
	}
	update, changes, err := buildBudgetUpdate(w, in)
	if err != nil {
		return RunBudgetUpdate{}, err
	}
	update.ID = w.ID
	updated, err := s.Update(ctx, update)
	if err != nil {
		return RunBudgetUpdate{}, err
	}
	liveUpdated := false
	if s.runner != nil {
		liveUpdated = s.runner.RefreshRunCaps(run.ID, updated)
	}
	return RunBudgetUpdate{
		RunID:       run.ID,
		WorkerID:    updated.ID,
		LiveUpdated: liveUpdated,
		Changes:     changes,
	}, nil
}

func buildBudgetUpdate(
	w *store.Worker, in DelegationBudgetInput,
) (UpdateInput, map[string]BudgetFieldChange, error) {
	changes := map[string]BudgetFieldChange{}
	var out UpdateInput
	var err error
	if out.MaxToolCalls, err = applyBudgetAxis(changes, "max_tool_calls", w.MaxToolCalls, in.MaxToolCalls, in.AdditionalToolCalls); err != nil {
		return UpdateInput{}, nil, err
	}
	if out.MaxWallClockSeconds, err = applyBudgetAxis(changes, "max_wall_clock_seconds", w.MaxWallClockSeconds, in.MaxWallClockSeconds, in.AdditionalWallClockSeconds); err != nil {
		return UpdateInput{}, nil, err
	}
	if out.MaxInputTokens, err = applyBudgetAxis(changes, "max_input_tokens", w.MaxInputTokens, in.MaxInputTokens, in.AdditionalInputTokens); err != nil {
		return UpdateInput{}, nil, err
	}
	if out.MaxOutputTokens, err = applyBudgetAxis(changes, "max_output_tokens", w.MaxOutputTokens, in.MaxOutputTokens, in.AdditionalOutputTokens); err != nil {
		return UpdateInput{}, nil, err
	}
	if len(changes) == 0 {
		return UpdateInput{}, nil, errors.New("budget increase did not change any caps")
	}
	return out, changes, nil
}

func applyBudgetAxis(
	changes map[string]BudgetFieldChange,
	name string,
	current int,
	absolute *int,
	additional int,
) (*int, error) {
	if absolute != nil && additional != 0 {
		return nil, fmt.Errorf("%s: use either absolute max or additional increment, not both", name)
	}
	if additional < 0 {
		return nil, fmt.Errorf("%s additional increment must be positive", name)
	}
	if absolute == nil && additional == 0 {
		return nil, nil
	}
	next := 0
	if absolute != nil {
		if *absolute <= 0 {
			return nil, fmt.Errorf("%s must be positive", name)
		}
		if current > 0 && *absolute <= current {
			return nil, fmt.Errorf("%s must increase current cap %d", name, current)
		}
		next = *absolute
	} else {
		if current <= 0 {
			return nil, fmt.Errorf("%s has no explicit current cap; use absolute max", name)
		}
		next = current + additional
	}
	changes[name] = BudgetFieldChange{Old: current, New: next}
	return &next, nil
}
