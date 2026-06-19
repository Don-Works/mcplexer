// template_install.go (M3) — install a Worker from a registry template.
// Companion to PublishAsTemplate. Given a (template_name, version) plus
// per-parameter values, a secret-scope binding, and a worker name, the
// installer assembles a CreateInput and calls Service.Create. The new
// Worker carries source_template_name + source_template_version so the
// dashboard can surface "vN available" hints later.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// resolveTemplateCapabilityProfileJSON resolves a worker template's optional
// capability preset + profile override into a validated, marshalled profile
// JSON. Returns "" when the template carries no scoping (today's behavior).
func resolveTemplateCapabilityProfileJSON(tmpl *workertemplates.WorkerTemplate) (string, error) {
	preset := strings.TrimSpace(tmpl.CapabilityPreset)
	if preset == "" && tmpl.CapabilityProfile == nil {
		return "", nil
	}
	base, ok := toolgate.ResolvePreset(preset)
	if !ok {
		return "", fmt.Errorf("template capability_preset %q is not one of full|coder|researcher|minimal", preset)
	}
	resolved := toolgate.Merge(base, tmpl.CapabilityProfile)
	if resolved == nil {
		return "", nil
	}
	if preset != "" {
		resolved.Preset = strings.ToLower(preset)
	}
	if err := validateCapabilityProfile(resolved); err != nil {
		return "", err
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		return "", fmt.Errorf("marshal template capability profile: %w", err)
	}
	return string(raw), nil
}

// InstallFromTemplateInput is the MCP / HTTP arg payload.
//
// SECRET BINDING — TWO PATHS:
//
//   - SecretScopeID (legacy, single binding) → the same scope is used
//     for every secret slot the template advertises. Practical only
//     when the template has exactly one secret slot (the model API
//     key).
//
//   - SecretBindings (M3.5+) → per-slot map { slot_name → scope_id }.
//     The slot named "model_api_key" wins for Worker.SecretScopeID
//     (the runner reads exactly that field today). Other slots are
//     ignored in M0–M3; multi-secret-aware workers will consume them
//     in a future release. When both fields are present the map
//     wins.
//
// At least one of (SecretScopeID, SecretBindings) MUST be populated.
type InstallFromTemplateInput struct {
	TemplateName    string            `json:"template_name"`
	TemplateVersion int               `json:"template_version,omitempty"` // 0 = latest
	WorkerName      string            `json:"worker_name"`
	WorkspaceID     string            `json:"workspace_id"`
	SecretScopeID   string            `json:"secret_scope_id,omitempty"`
	SecretBindings  map[string]string `json:"secret_bindings,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	ScheduleSpec    string            `json:"schedule_spec,omitempty"`
	ExecMode        string            `json:"exec_mode,omitempty"`
	Enabled         *bool             `json:"enabled,omitempty"`
}

// modelAPIKeySlotName is the well-known secret-slot name the runner
// reads as Worker.SecretScopeID. Multi-secret-aware Workers (M3.5+)
// will consume additional named slots; for now everything but this
// slot is recorded for forward-compat but does not flow into the
// Worker row.
//
// TODO(M3.5+): expose multi-slot consumption to the runner so models
// that need multiple API keys (e.g. provider + observability) can
// resolve them by slot name.
const modelAPIKeySlotName = "model_api_key"

// InstallFromTemplate creates a Worker from a registry template. The new
// Worker's source_template_* columns record the install lineage.
func (s *Service) InstallFromTemplate(
	ctx context.Context, in InstallFromTemplateInput,
) (*store.Worker, error) {
	if s.templates == nil {
		return nil, errors.New("template publisher not wired")
	}
	if err := validateInstallInput(in); err != nil {
		return nil, err
	}
	// G10 — restrict the template lookup scope to the destination
	// workspace (+ globals). Without this scope, AdminScope would let a
	// worker in workspace alpha install a template registered in
	// workspace beta — a cross-tenant reference. The store's lookup
	// honours shadowing: a workspace-scoped template hides the global
	// of the same name, so behaviour is unchanged for the common case.
	entry, tmpl, err := s.loadTemplateInScope(ctx,
		store.SkillScope{WorkspaceIDs: []string{in.WorkspaceID}},
		in.TemplateName, in.TemplateVersion)
	if err != nil {
		return nil, err
	}
	if err := validateRequiredParameters(tmpl, in.Parameters); err != nil {
		return nil, err
	}
	createInput, err := s.buildCreateInputFromTemplate(tmpl, entry, in)
	if err != nil {
		return nil, err
	}
	return s.createFromTemplate(ctx, createInput, entry.Name, entry.Version)
}

// validateInstallInput catches obviously-missing fields early so callers
// get one error rather than three buried inside Create.
func validateInstallInput(in InstallFromTemplateInput) error {
	if strings.TrimSpace(in.TemplateName) == "" {
		return errors.New("template_name required")
	}
	if strings.TrimSpace(in.WorkerName) == "" {
		return errors.New("worker_name required")
	}
	if strings.TrimSpace(in.WorkspaceID) == "" {
		return errors.New("workspace_id required")
	}
	if _, err := resolveModelAPIKeyScope(in); err != nil {
		return err
	}
	return nil
}

// resolveModelAPIKeyScope picks the scope_id the runner needs as
// Worker.SecretScopeID. Priority:
//
//  1. SecretBindings[model_api_key] (the per-slot map; M3.5+ path)
//  2. SecretScopeID (legacy single-slot path)
//
// Returns an error when neither is set — every Worker needs a model
// API key today so the install must surface that immediately rather
// than create a half-configured row.
func resolveModelAPIKeyScope(in InstallFromTemplateInput) (string, error) {
	if v := strings.TrimSpace(in.SecretBindings[modelAPIKeySlotName]); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(in.SecretScopeID); v != "" {
		return v, nil
	}
	return "", errors.New(
		"secret binding required for model_api_key (pass secret_bindings.model_api_key or secret_scope_id)",
	)
}

// loadTemplateInScope fetches the registry entry + decodes the
// WorkerTemplate JSON body. scope restricts the lookup so a workspace's
// install cannot pull a template registered in a different workspace
// (G10). Pass workertemplates.AdminScope() to bypass scoping — kept
// available for admin/import paths if a future caller needs it.
func (s *Service) loadTemplateInScope(
	ctx context.Context, scope store.SkillScope, name string, version int,
) (*store.WorkerTemplateEntry, *workertemplates.WorkerTemplate, error) {
	ref := workertemplates.VersionRef{Latest: true}
	if version > 0 {
		ref = workertemplates.VersionRef{Version: version}
	}
	entry, err := s.templates.Get(ctx, scope, name, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("load template: %w", err)
	}
	tmpl, err := workertemplates.Unmarshal(entry.Body)
	if err != nil {
		return nil, nil, err
	}
	return entry, tmpl, nil
}

// validateRequiredParameters returns an error when a template parameter
// is marked Required and the caller didn't pass a value (or passed an
// empty string). Defaulted optional params are filled later.
func validateRequiredParameters(
	tmpl *workertemplates.WorkerTemplate, params map[string]string,
) error {
	var missing []string
	for _, p := range tmpl.ParameterSchema {
		if !p.Required {
			continue
		}
		if strings.TrimSpace(params[p.Name]) == "" {
			missing = append(missing, p.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required parameter(s): %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// buildCreateInputFromTemplate assembles a CreateInput from the template
// plus the caller-supplied overrides. Hints are used when the caller
// didn't override; the prompt_template is rendered verbatim from the
// registry and the parameters_json carries the user-supplied values.
func (s *Service) buildCreateInputFromTemplate(
	tmpl *workertemplates.WorkerTemplate,
	_ *store.WorkerTemplateEntry,
	in InstallFromTemplateInput,
) (CreateInput, error) {
	params := mergeParameterDefaults(tmpl, in.Parameters)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return CreateInput{}, fmt.Errorf("marshal parameters: %w", err)
	}
	allowJSON, err := json.Marshal(tmpl.ToolAllowlist)
	if err != nil {
		return CreateInput{}, fmt.Errorf("marshal tool_allowlist: %w", err)
	}
	channelsJSON, err := json.Marshal(tmpl.OutputChannelsHint)
	if err != nil {
		return CreateInput{}, fmt.Errorf("marshal output_channels: %w", err)
	}
	capabilityJSON, err := resolveTemplateCapabilityProfileJSON(tmpl)
	if err != nil {
		return CreateInput{}, err
	}
	scheduleSpec := defaultStr(strings.TrimSpace(in.ScheduleSpec), tmpl.ScheduleSpecHint)
	execMode := defaultStr(strings.TrimSpace(in.ExecMode), tmpl.ExecModeHint)
	// resolveModelAPIKeyScope already validated this; ignore the error
	// here since validateInstallInput would have aborted earlier.
	scopeID, _ := resolveModelAPIKeyScope(in)
	return CreateInput{
		Name:                  in.WorkerName,
		Description:           tmpl.Description,
		ModelProvider:         tmpl.ModelProviderHint,
		ModelID:               tmpl.ModelIDHint,
		SecretScopeID:         scopeID,
		SkillName:             tmpl.SkillName,
		SkillVersion:          tmpl.SkillVersion,
		PromptTemplate:        tmpl.PromptTemplate,
		ParametersJSON:        string(paramsJSON),
		ScheduleSpec:          scheduleSpec,
		ToolAllowlistJSON:     string(allowJSON),
		CapabilityProfileJSON: capabilityJSON,
		OutputChannelsJSON:    string(channelsJSON),
		ExecMode:              execMode,
		Enabled:               in.Enabled,
		WorkspaceID:           in.WorkspaceID,
		// Lineage fields are stamped by createFromTemplate; not exposed
		// to caller-driven payloads.
	}, nil
}

// mergeParameterDefaults composes the final parameters map: caller values
// win, template defaults fill the rest.
func mergeParameterDefaults(
	tmpl *workertemplates.WorkerTemplate, given map[string]string,
) map[string]string {
	out := map[string]string{}
	for _, p := range tmpl.ParameterSchema {
		if p.Default != "" {
			out[p.Name] = p.Default
		}
	}
	for k, v := range given {
		out[k] = v
	}
	return out
}
