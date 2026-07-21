package admin

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type WaitForDelegationInput struct {
	DelegationID   string `json:"delegation_id"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	PollIntervalMS int    `json:"poll_interval_ms,omitempty"`
}

type WaitForDelegationOutput struct {
	DelegationID string `json:"delegation_id"`
	Status       string `json:"status"`
	TimedOut     bool   `json:"timed_out,omitempty"`

	Workers     int `json:"workers"`
	Success     int `json:"success"`
	Failure     int `json:"failure"`
	Blocked     int `json:"blocked,omitempty"`
	Cancelled   int `json:"cancelled,omitempty"`
	Interrupted int `json:"interrupted,omitempty"`
	Running     int `json:"running"`

	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	ToolCalls    int     `json:"tool_calls,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
}

func (s *Service) WaitForDelegation(ctx context.Context, in WaitForDelegationInput) (WaitForDelegationOutput, error) {
	in.DelegationID = strings.TrimSpace(in.DelegationID)
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	if in.DelegationID == "" {
		return WaitForDelegationOutput{}, fmt.Errorf(
			"delegation_id required, e.g. %s",
			`{"delegation_id":"del-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"}`,
		)
	}
	timeoutSec := in.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 300
	}
	if timeoutSec > 600 {
		return WaitForDelegationOutput{}, fmt.Errorf("timeout_seconds max 600, got %d", timeoutSec)
	}
	pollInterval, err := delegationPollIntervalFromMillis(in.PollIntervalMS)
	if err != nil {
		return WaitForDelegationOutput{}, err
	}
	pollTimeout := time.Duration(timeoutSec) * time.Second

	dctx, err := s.pollDelegationTerminal(ctx, in.DelegationID, in.WorkspaceID, pollTimeout, pollInterval)
	if err != nil {
		return WaitForDelegationOutput{}, err
	}
	isTimedOut := !isTerminalStatus(dctx.Status)
	return WaitForDelegationOutput{
		DelegationID: in.DelegationID,
		Status:       dctx.Status,
		TimedOut:     isTimedOut,
		Workers:      dctx.Aggregate.Workers,
		Success:      dctx.Aggregate.Success,
		Failure:      dctx.Aggregate.Failure,
		Blocked:      dctx.Aggregate.Blocked,
		Cancelled:    dctx.Aggregate.Cancelled,
		Interrupted:  dctx.Aggregate.Interrupted,
		Running:      dctx.Aggregate.Running,
		InputTokens:  dctx.Aggregate.InputTokens,
		OutputTokens: dctx.Aggregate.OutputTokens,
		CostUSD:      dctx.Aggregate.CostUSD,
		ToolCalls:    dctx.Aggregate.ToolCalls,
		DurationMS:   dctx.Aggregate.DurationMS,
	}, nil
}

func isTerminalStatus(status string) bool {
	switch status {
	case "success", "partial", "failure", "needs_review", "blocked", "cancelled", "interrupted":
		return true
	}
	return false
}

const delegationPollInterval = 2 * time.Second

func delegationPollIntervalFromMillis(ms int) (time.Duration, error) {
	if ms <= 0 {
		return delegationPollInterval, nil
	}
	if ms < 500 {
		return 0, fmt.Errorf("poll_interval_ms min 500, got %d", ms)
	}
	if ms > 10000 {
		return 0, fmt.Errorf("poll_interval_ms max 10000, got %d", ms)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func (s *Service) pollDelegationTerminal(
	ctx context.Context,
	delegationID string,
	workspaceID string,
	timeout time.Duration,
	pollInterval time.Duration,
) (DelegationContext, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if pollInterval <= 0 {
		pollInterval = delegationPollInterval
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastStatus := ""
	var last DelegationContext
	haveLast := false
	for {
		select {
		case <-ctx.Done():
			if haveLast {
				return last, nil
			}
			return DelegationContext{}, ctx.Err()
		default:
		}

		dctx, found, err := s.findDelegationByID(ctx, delegationID, workspaceID)
		if err != nil {
			if ctx.Err() != nil && haveLast {
				return last, nil
			}
			return DelegationContext{}, err
		}
		if !found {
			return DelegationContext{}, fmt.Errorf("delegation %s not found", delegationID)
		}
		last = dctx
		haveLast = true
		if dctx.Status != lastStatus {
			lastStatus = dctx.Status
		}
		if isTerminalStatus(dctx.Status) {
			return dctx, nil
		}
		select {
		case <-ctx.Done():
			return dctx, nil
		case <-ticker.C:
		}
	}
}

func (s *Service) findDelegationByID(ctx context.Context, id string, workspaceID string) (DelegationContext, bool, error) {
	delegations, err := s.ListDelegations(ctx, DelegationListInput{WorkspaceID: workspaceID, Limit: 200})
	if err != nil {
		return DelegationContext{}, false, err
	}
	for _, d := range delegations {
		if d.ID == id {
			return d, true, nil
		}
	}
	return DelegationContext{}, false, nil
}
