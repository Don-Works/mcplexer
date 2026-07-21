package admin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/toolgate"
)

type InvokeModelInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Objective   string `json:"objective"`
	Handoff     string `json:"handoff,omitempty"`

	ModelProfileID   string `json:"model_profile_id,omitempty"`
	ModelProvider    string `json:"model_provider,omitempty"`
	ModelID          string `json:"model_id,omitempty"`
	ModelEndpointURL string `json:"model_endpoint_url,omitempty"`
	SecretScopeID    string `json:"secret_scope_id,omitempty"`

	WorkerMode      string   `json:"worker_mode,omitempty"`
	WorkerIsolation string   `json:"worker_isolation,omitempty"`
	TouchesFiles    []string `json:"touches_files,omitempty"`

	MaxWallClockSeconds int `json:"max_wall_clock_seconds,omitempty"`
	MaxToolCalls        int `json:"max_tool_calls,omitempty"`
	MaxOutputTokens     int `json:"max_output_tokens,omitempty"`

	WaitSeconds int `json:"wait_seconds,omitempty"`

	TaskID   string `json:"task_id,omitempty"`
	TaskKind string `json:"task_kind,omitempty"`

	ToolAllowlistJSON string `json:"tool_allowlist_json,omitempty"`

	CapabilityPreset  string                      `json:"capability_preset,omitempty"`
	CapabilityProfile *toolgate.CapabilityProfile `json:"capability_profile,omitempty"`

	DisabledProviders map[string]bool `json:"-"`
}

type InvokeModelOutput struct {
	DelegationID string `json:"delegation_id"`
	Status       string `json:"status"`
	TimedOut     bool   `json:"timed_out,omitempty"`
	WorkerStatus string `json:"worker_status,omitempty"`

	OutputText string `json:"output_text,omitempty"`
	Error      string `json:"error,omitempty"`

	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	ToolCalls    int     `json:"tool_calls,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`

	ModelProvider string `json:"model_provider,omitempty"`
	ModelID       string `json:"model_id,omitempty"`
	ResultBranch  string `json:"result_branch,omitempty"`
	ResultCommit  string `json:"result_commit,omitempty"`
	ResultChanged bool   `json:"result_changed,omitempty"`
	// Warnings passes through the underlying delegation's creation-time
	// advisories (frontier-class model, file-claim overlaps).
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Service) InvokeModel(ctx context.Context, in InvokeModelInput) (InvokeModelOutput, error) {
	in.Objective = strings.TrimSpace(in.Objective)
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	if in.Objective == "" {
		return InvokeModelOutput{}, fmt.Errorf("objective required, e.g. %s",
			`{"objective":"Review the route handler for missing auth checks."}`)
	}

	// max_wall_clock_seconds controls the *worker* lifetime (passed through to Delegate).
	// wait_seconds controls how long *this* invoke call will poll before returning
	// (to avoid blocking the code-mode sandbox past its cap and losing the delegation_id).
	wallSecs := in.MaxWallClockSeconds
	if wallSecs <= 0 {
		wallSecs = defaultDelegationMaxWallClockSeconds
	}
	waitSecs := in.WaitSeconds
	if waitSecs <= 0 {
		waitSecs = 25
	}
	if waitSecs > 600 {
		waitSecs = 600
	}
	pollTimeout := time.Duration(waitSecs) * time.Second

	reviewRequired := false
	delIn := DelegationInput{
		WorkspaceID:     in.WorkspaceID,
		Objective:       in.Objective,
		Handoff:         in.Handoff,
		Name:            "fire-and-collect",
		TaskID:          in.TaskID,
		TaskKind:        in.TaskKind,
		WorkerMode:      in.WorkerMode,
		WorkerIsolation: in.WorkerIsolation,
		TouchesFiles:    append([]string(nil), in.TouchesFiles...),
		ReviewRequired:  &reviewRequired,

		ModelProfileID:   in.ModelProfileID,
		ModelProvider:    in.ModelProvider,
		ModelID:          in.ModelID,
		ModelEndpointURL: in.ModelEndpointURL,
		SecretScopeID:    in.SecretScopeID,

		Parallelism:         1,
		MaxWallClockSeconds: wallSecs,
		MaxToolCalls:        in.MaxToolCalls,
		MaxOutputTokens:     in.MaxOutputTokens,
		ToolAllowlistJSON:   in.ToolAllowlistJSON,
		CapabilityPreset:    in.CapabilityPreset,
		CapabilityProfile:   in.CapabilityProfile,

		DisabledProviders: in.DisabledProviders,

		ModelSelectionMode: "single",
	}
	out, err := s.Delegate(ctx, delIn)
	if err != nil {
		return InvokeModelOutput{}, err
	}

	// Poll is bounded by the (clamped) wait_seconds so the invoke call itself
	// always returns promptly. On timeout with run still active we return
	// success (with delegation_id + timed_out:true) — never an error, never drop the id.
	dctx, pollErr := s.pollDelegationTerminal(ctx, out.DelegationID, out.WorkspaceID, pollTimeout, delegationPollInterval)

	o := InvokeModelOutput{
		DelegationID: out.DelegationID,
		Warnings:     out.Warnings,
	}
	o.Status = dctx.Status
	if o.Status == "" {
		o.Status = "dispatched"
	}
	o.TimedOut = !isTerminalStatus(o.Status)

	if pollErr == nil {
		if len(dctx.Workers) > 0 {
			w := dctx.Workers[0]
			o.WorkerStatus = workerSummaryStatus(w)
			o.InputTokens = dctx.Aggregate.InputTokens
			o.OutputTokens = dctx.Aggregate.OutputTokens
			o.CostUSD = dctx.Aggregate.CostUSD
			o.ToolCalls = dctx.Aggregate.ToolCalls
			o.DurationMS = dctx.Aggregate.DurationMS
			if w.LatestRun != nil {
				o.OutputText = w.LatestRun.OutputText
				o.Error = w.LatestRun.Error
				o.ModelProvider = w.LatestRun.ModelProvider
				o.ModelID = w.LatestRun.ModelID
				o.ResultBranch = w.LatestRun.ResultBranch
				o.ResultCommit = w.LatestRun.ResultCommit
				o.ResultChanged = w.LatestRun.ResultChanged
			}
			if w.Worker != nil {
				if o.ModelProvider == "" {
					o.ModelProvider = w.Worker.ModelProvider
				}
				if o.ModelID == "" {
					o.ModelID = w.Worker.ModelID
				}
			}
		}
	}
	// On poll timeout (or transient poll issue) we still return success with id + timed_out.
	// Caller must poll mcpx__list_delegations (or mcpx__wait_for_delegation) using the id.
	return o, nil
}

func workerSummaryStatus(w DelegationWorkerContext) string {
	if w.DispatchFailed {
		return "dispatch_failed"
	}
	if w.LatestRun != nil {
		return w.LatestRun.Status
	}
	return "dispatched"
}
