package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// CreateInput is the mcplexer__create_worker arg payload. All fields are
// json-tagged so we can decode straight from the tool args.
//
// SkillRefs (M0.7+) is the canonical multi-skill list — when set it
// overrides the legacy SkillName / SkillVersion pair. Both shapes are
// accepted on create / update so existing agents and templates keep
// working without an audit-wide rewrite.
//
// SourceTemplateName / SourceTemplateVersion are deliberately NOT
// exposed on CreateInput — they're set only by the internal
// createFromTemplate path. Operator-supplied create payloads cannot
// forge template lineage. See internal/workers/admin/template_install.go
// for the template flow that populates these.
type CreateInput struct {
	Name                  string                        `json:"name"`
	Description           string                        `json:"description,omitempty"`
	ModelProvider         string                        `json:"model_provider"`
	ModelID               string                        `json:"model_id"`
	ModelEndpointURL      string                        `json:"model_endpoint_url,omitempty"`
	SecretScopeID         string                        `json:"secret_scope_id"`
	SkillName             string                        `json:"skill_name,omitempty"`
	SkillVersion          string                        `json:"skill_version,omitempty"`
	SkillRefs             []store.SkillRef              `json:"skill_refs,omitempty"`
	PromptTemplate        string                        `json:"prompt_template"`
	ParametersJSON        string                        `json:"parameters_json,omitempty"`
	ScheduleSpec          string                        `json:"schedule_spec"`
	ToolAllowlistJSON     string                        `json:"tool_allowlist_json,omitempty"`
	CapabilityProfileJSON string                        `json:"capability_profile_json,omitempty"`
	OutputChannelsJSON    string                        `json:"output_channels_json,omitempty"`
	ExecMode              string                        `json:"exec_mode,omitempty"`
	ConcurrencyPolicy     string                        `json:"concurrency_policy,omitempty"`
	MemoryScopeID         string                        `json:"memory_scope_id,omitempty"`
	Enabled               *bool                         `json:"enabled,omitempty"`
	WorkspaceID           string                        `json:"workspace_id"`
	WorkspaceAccess       []store.WorkerWorkspaceAccess `json:"workspace_access,omitempty"`

	// Per-worker safety caps (M1). 0 in run caps means "use the runner
	// default"; 0 in budget/failure caps means "no cap".
	MaxInputTokens         int     `json:"max_input_tokens,omitempty"`
	MaxOutputTokens        int     `json:"max_output_tokens,omitempty"`
	MaxToolCalls           int     `json:"max_tool_calls,omitempty"`
	MaxWallClockSeconds    int     `json:"max_wall_clock_seconds,omitempty"`
	MaxMonthlyCostUSD      float64 `json:"max_monthly_cost_usd,omitempty"`
	MaxConsecutiveFailures int     `json:"max_consecutive_failures,omitempty"`

	// sourceTemplateName / sourceTemplateVersion are populated only by
	// the internal createFromTemplate path (see template_install.go).
	// They're unexported so JSON decoding can never set them — the MCP
	// surface explicitly omits these fields from its create schema.
	sourceTemplateName    string
	sourceTemplateVersion int
}

// Create validates, defaults, and persists a new Worker. Returns the
// stored row (with generated id + timestamps). Emits worker_admin.create
// (status="ok" on success, "error" on store failure) so every mutation
// to the worker catalog leaves an audit trail.
func (s *Service) Create(ctx context.Context, in CreateInput) (*store.Worker, error) {
	w, err := s.buildWorkerFromCreate(in)
	if err != nil {
		// Validation failures pre-store: don't emit (no worker_id yet,
		// nothing landed in the catalog). The mcplexer__create_worker
		// MCP-dispatch audit row (one layer up) already records the
		// attempt + error message.
		return nil, err
	}
	if err := s.store.CreateWorker(ctx, w); err != nil {
		s.emitAuditCreate(ctx, w, "error", err.Error())
		return nil, translateConstraintError(err, w)
	}
	// Re-read so callers see exactly what's on disk (timestamps + any
	// defaults applied by the sqlite store on insert).
	stored, err := s.store.GetWorker(ctx, w.ID)
	if err != nil {
		s.emitAuditCreate(ctx, w, "error", err.Error())
		return nil, err
	}
	s.emitAuditCreate(ctx, stored, "ok", "")
	if stored.Enabled {
		s.syncScheduleAfterChange(ctx, stored)
	}
	return stored, nil
}

// buildWorkerFromCreate + resolveSkillRefs live in crud_build.go to
// keep this file under the 300-line cap.

// UpdateInput is the mcplexer__update_worker arg payload. Optional
// fields are *string / *bool so we can detect presence and leave omitted
// fields untouched.
type UpdateInput struct {
	ID               string  `json:"id"`
	Name             *string `json:"name,omitempty"`
	Description      *string `json:"description,omitempty"`
	ModelProvider    *string `json:"model_provider,omitempty"`
	ModelID          *string `json:"model_id,omitempty"`
	ModelEndpointURL *string `json:"model_endpoint_url,omitempty"`
	SecretScopeID    *string `json:"secret_scope_id,omitempty"`
	SkillName        *string `json:"skill_name,omitempty"`
	SkillVersion     *string `json:"skill_version,omitempty"`
	// SkillRefs is the canonical multi-skill list. When non-nil it
	// overrides SkillName / SkillVersion (which exist only for backward
	// compat). A non-nil zero-length slice clears every attached skill.
	SkillRefs             *[]store.SkillRef              `json:"skill_refs,omitempty"`
	PromptTemplate        *string                        `json:"prompt_template,omitempty"`
	ParametersJSON        *string                        `json:"parameters_json,omitempty"`
	ScheduleSpec          *string                        `json:"schedule_spec,omitempty"`
	ToolAllowlistJSON     *string                        `json:"tool_allowlist_json,omitempty"`
	CapabilityProfileJSON *string                        `json:"capability_profile_json,omitempty"`
	OutputChannelsJSON    *string                        `json:"output_channels_json,omitempty"`
	ExecMode              *string                        `json:"exec_mode,omitempty"`
	ConcurrencyPolicy     *string                        `json:"concurrency_policy,omitempty"`
	MemoryScopeID         *string                        `json:"memory_scope_id,omitempty"`
	Enabled               *bool                          `json:"enabled,omitempty"`
	WorkspaceID           *string                        `json:"workspace_id,omitempty"`
	WorkspaceAccess       *[]store.WorkerWorkspaceAccess `json:"workspace_access,omitempty"`

	// Per-worker safety caps (M1). Pointer-typed so omitted fields are
	// left untouched; passing 0 explicitly clears run caps to the runner
	// default and budget/failure caps to "no cap".
	MaxInputTokens         *int     `json:"max_input_tokens,omitempty"`
	MaxOutputTokens        *int     `json:"max_output_tokens,omitempty"`
	MaxToolCalls           *int     `json:"max_tool_calls,omitempty"`
	MaxWallClockSeconds    *int     `json:"max_wall_clock_seconds,omitempty"`
	MaxMonthlyCostUSD      *float64 `json:"max_monthly_cost_usd,omitempty"`
	MaxConsecutiveFailures *int     `json:"max_consecutive_failures,omitempty"`
	// AutoPausedReason is operator-clearable: editing the worker (e.g.
	// re-enabling after manual review) sets this to "" so the dashboard
	// stops showing the auto-pause banner.
	AutoPausedReason *string `json:"auto_paused_reason,omitempty"`
}

// Update applies a partial patch to the named worker. Returns the
// post-update row read back from the store. Emits worker_admin.update
// with a per-field {old,new} diff so reviewers see exactly what changed
// (long fields rendered as fingerprints, not bodies).
func (s *Service) Update(ctx context.Context, in UpdateInput) (*store.Worker, error) {
	if strings.TrimSpace(in.ID) == "" {
		return nil, errors.New("id required")
	}
	w, err := s.store.GetWorker(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// Snapshot the pre-update state so the audit row can render the
	// "old" side of every mutated field. applyUpdate mutates w in place.
	oldSnapshot := *w
	diff := buildUpdateDiff(&oldSnapshot, in)
	applyUpdate(w, in)
	if err := s.validateUpdate(w, in); err != nil {
		return nil, err
	}
	if err := s.store.UpdateWorker(ctx, w); err != nil {
		s.emitAuditUpdate(ctx, w.ID, diff, "error", err.Error())
		return nil, translateConstraintError(err, w)
	}
	stored, err := s.store.GetWorker(ctx, w.ID)
	if err != nil {
		s.emitAuditUpdate(ctx, w.ID, diff, "error", err.Error())
		return nil, err
	}
	s.emitAuditUpdate(ctx, stored.ID, diff, "ok", "")
	if stored.Enabled {
		s.syncScheduleAfterChange(ctx, stored)
	} else {
		s.removeScheduleAfterDelete(ctx, stored.ID)
	}
	return stored, nil
}

// validateUpdate re-runs every load-bearing validator against the merged
// worker state (post-applyUpdate). Pointer-presence on `in` gates the
// per-field whitelists so an update only re-checks what it touched, while
// the safety caps + memory-scope are validated against final state
// unconditionally. Without this, Update was an asymmetric bypass of the
// Create-side guards: the SQLite columns have no CHECK constraint and
// applyWorkerDefaults only fills EMPTY values, so a bad exec_mode /
// concurrency_policy / negative cap would persist unchecked.
func (s *Service) validateUpdate(w *store.Worker, in UpdateInput) error {
	if w.ModelProvider != "" {
		if err := validateModelProvider(w.ModelProvider, w.ModelEndpointURL); err != nil {
			return err
		}
	}
	if in.ExecMode != nil {
		if err := validateExecMode(w.ExecMode); err != nil {
			return err
		}
	}
	if in.ConcurrencyPolicy != nil {
		if err := validateConcurrencyPolicy(w.ConcurrencyPolicy); err != nil {
			return err
		}
	}
	if err := validateCaps(
		w.MaxInputTokens, w.MaxOutputTokens, w.MaxToolCalls,
		w.MaxWallClockSeconds, w.MaxMonthlyCostUSD, w.MaxConsecutiveFailures,
	); err != nil {
		return err
	}
	if in.ScheduleSpec != nil {
		if err := s.validateScheduleSpec(w.ScheduleSpec); err != nil {
			return fmt.Errorf("invalid schedule_spec: %w", err)
		}
	}
	if in.ToolAllowlistJSON != nil {
		if err := validateAllowlistJSON(*in.ToolAllowlistJSON); err != nil {
			return err
		}
	}
	if in.CapabilityProfileJSON != nil {
		if err := validateCapabilityProfileJSON(*in.CapabilityProfileJSON); err != nil {
			return err
		}
	}
	if in.OutputChannelsJSON != nil {
		if err := validateOutputChannelsJSON(*in.OutputChannelsJSON); err != nil {
			return err
		}
	}
	if in.ParametersJSON != nil {
		if err := validateParametersJSON(*in.ParametersJSON); err != nil {
			return err
		}
		// Empty string → "{}" so the runner always sees a JSON object.
		if strings.TrimSpace(w.ParametersJSON) == "" {
			w.ParametersJSON = "{}"
		}
	}
	if in.SkillRefs != nil {
		if err := validateSkillRefs(*in.SkillRefs); err != nil {
			return err
		}
	}
	if in.WorkspaceAccess != nil {
		if err := validateWorkspaceAccess(*in.WorkspaceAccess); err != nil {
			return err
		}
	}
	if in.PromptTemplate != nil {
		if err := validatePromptTemplate(w.PromptTemplate); err != nil {
			return err
		}
	}
	// G12 — re-validate memory_scope after applyUpdate applied both the
	// (optional) MemoryScopeID and WorkspaceID, so the check runs against
	// final state.
	return validateMemoryScopeSameWorkspace(w.MemoryScopeID, w.WorkspaceID)
}

// createFromTemplate is the internal-only Create variant that stamps
// source_template_name + source_template_version on the new Worker.
// Used by InstallFromTemplate to record the registry-template lineage;
// operator-supplied create payloads CANNOT reach this path because the
// sourceTemplate fields on CreateInput are unexported.
func (s *Service) createFromTemplate(
	ctx context.Context, in CreateInput, name string, version int,
) (*store.Worker, error) {
	in.sourceTemplateName = name
	in.sourceTemplateVersion = version
	return s.Create(ctx, in)
}

// Delete hard-deletes a worker. Runs are intentionally preserved by the
// store layer; we don't touch them here. Emits worker_admin.delete with
// the worker name captured BEFORE the delete (the row is gone after).
func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("id required")
	}
	// Best-effort name lookup for the audit payload. If the worker
	// doesn't exist the subsequent DeleteWorker will surface the right
	// error; we just log "name" empty in the audit row.
	name := ""
	if w, err := s.store.GetWorker(ctx, id); err == nil && w != nil {
		name = w.Name
	}
	if err := s.store.DeleteWorker(ctx, id); err != nil {
		s.emitAuditDelete(ctx, id, name, "error", err.Error())
		return err
	}
	s.emitAuditDelete(ctx, id, name, "ok", "")
	s.removeScheduleAfterDelete(ctx, id)
	return nil
}

// SetEnabled, Pause, Resume, and the shared setEnabledWithVerb live in
// crud_enable.go to keep this file under the 300-line budget.
