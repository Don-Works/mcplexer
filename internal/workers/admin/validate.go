package admin

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// Valid model providers — mirrors models.Provider* but kept local so
// admin doesn't import the heavyweight models package just for a few
// strings.
const (
	providerAnthropic    = "anthropic"
	providerOpenAI       = "openai"
	providerOpenAICompat = "openai_compat"
	// providerClaudeCLI shells out to the host's `claude` binary and
	// bills against whatever credentials that install uses (OAuth
	// subscription or ANTHROPIC_API_KEY). Workers using this provider
	// still need a SecretScopeID at the schema level (placeholder is
	// fine — pass any existing scope) because the workers schema
	// declares secret_scope_id NOT NULL; the runner short-circuits on
	// empty scope content so no key is actually required.
	providerClaudeCLI = "claude_cli"
	// providerOpenCodeCLI shells out to the host's `opencode` binary.
	// Like claude_cli, the workers schema's NOT NULL secret_scope_id
	// must be satisfied with any placeholder scope — opencode owns its
	// own credential storage and ignores whatever's in the scope.
	providerOpenCodeCLI = "opencode_cli"
	// providerGrokCLI shells out to xAI's host-installed `grok` binary.
	// It uses `grok login` or XAI_API_KEY, so the worker secret scope is
	// also just a schema placeholder.
	providerGrokCLI = "grok_cli"
	// providerMiMoCLI shells out to Xiaomi's host-installed `mimo` binary.
	// It uses `mimo providers login`, so the worker secret scope is also
	// just a schema placeholder.
	providerMiMoCLI = "mimo_cli"
	// providerGeminiCLI shells out to Google's host-installed `gemini` binary.
	// It uses GEMINI_API_KEY or `gemini auth`, so the worker secret scope is
	// also just a schema placeholder.
	providerGeminiCLI = "gemini_cli"
	providerCodexCLI  = "codex_cli"
	// providerPiCLI shells out to the host's `pi` binary (the Pi coding
	// harness). Models + providers are configured in ~/.pi/agent/models.json,
	// so the worker secret scope is also just a schema placeholder.
	providerPiCLI = "pi_cli"

	maxWorkerPromptTemplateBytes = 128 * 1024
)

// validateMemoryScopeSameWorkspace — G12. The memory subsystem is
// workspace-scoped; a worker.memory_scope_id pointing at a different
// workspace's slice would be a cross-tenant read. The model has no
// dedicated memory_scope entity today, so the binding is convention:
// memory_scope_id is interpreted as a workspace_id. Reject anything
// other than empty (= use worker's workspace) or the worker's own
// workspace_id.
func validateMemoryScopeSameWorkspace(memoryScopeID, workspaceID string) error {
	memoryScopeID = strings.TrimSpace(memoryScopeID)
	if memoryScopeID == "" {
		return nil
	}
	if memoryScopeID != strings.TrimSpace(workspaceID) {
		return fmt.Errorf(
			"memory_scope_id %q must equal workspace_id %q (cross-workspace memory access is denied)",
			memoryScopeID, workspaceID,
		)
	}
	return nil
}

// validateCreate covers every "required" field plus the cross-field
// dependencies (openai_compat requires model_endpoint_url).
func validateCreate(in CreateInput) error {
	type check struct {
		field string
		val   string
	}
	for _, c := range []check{
		{"name", in.Name},
		{"model_provider", in.ModelProvider},
		{"model_id", in.ModelID},
		{"secret_scope_id", in.SecretScopeID},
		{"prompt_template", in.PromptTemplate},
		{"schedule_spec", in.ScheduleSpec},
		{"workspace_id", in.WorkspaceID},
	} {
		if strings.TrimSpace(c.val) == "" {
			return missingFieldExampleError(c.field)
		}
	}
	if err := validateModelProvider(in.ModelProvider, in.ModelEndpointURL); err != nil {
		return err
	}
	if err := validatePromptTemplate(in.PromptTemplate); err != nil {
		return err
	}
	if in.ExecMode != "" {
		if err := validateExecMode(in.ExecMode); err != nil {
			return err
		}
	}
	if in.ConcurrencyPolicy != "" {
		if err := validateConcurrencyPolicy(in.ConcurrencyPolicy); err != nil {
			return err
		}
	}
	if err := validateCaps(
		in.MaxInputTokens, in.MaxOutputTokens, in.MaxToolCalls,
		in.MaxWallClockSeconds, in.MaxMonthlyCostUSD, in.MaxConsecutiveFailures,
	); err != nil {
		return err
	}
	if err := validateAllowlistJSON(in.ToolAllowlistJSON); err != nil {
		return err
	}
	if err := validateCapabilityProfileJSON(in.CapabilityProfileJSON); err != nil {
		return err
	}
	if err := validateOutputChannelsJSON(in.OutputChannelsJSON); err != nil {
		return err
	}
	if err := validateParametersJSON(in.ParametersJSON); err != nil {
		return err
	}
	if err := validateSkillRefs(in.SkillRefs); err != nil {
		return err
	}
	if err := validateWorkspaceAccess(in.WorkspaceAccess); err != nil {
		return err
	}
	if err := validateMemoryScopeSameWorkspace(in.MemoryScopeID, in.WorkspaceID); err != nil {
		return err
	}
	return nil
}

// missingFieldExampleError returns "<field> required" and, for the
// three model-related fields that cheap LLMs get wrong most often
// (model_provider, model_id, secret_scope_id), appends a copy-pasteable
// corrected example derived from the accepted enum values in this file
// (anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli). The
// examples mirror the ones in delegation_models.go so a model that
// hits either path (create or delegate) sees the same hint.
func missingFieldExampleError(field string) error {
	switch field {
	case "model_provider":
		return errors.New(
			"model_provider required. Example: {\"name\":\"digest-bot\", \"model_provider\":\"opencode_cli\", \"model_id\":\"minimax/MiniMax-M3\"}")
	case "model_id":
		return errors.New(
			"model_id required. Example: {\"name\":\"digest-bot\", \"model_provider\":\"opencode_cli\", \"model_id\":\"minimax/MiniMax-M3\"}")
	case "secret_scope_id":
		return errors.New(
			"secret_scope_id required. Example: {\"name\":\"digest-bot\", \"model_provider\":\"anthropic\", \"model_id\":\"claude-sonnet-4-5\", \"secret_scope_id\":\"scope-anthropic-prod\"}")
	default:
		return fmt.Errorf("%s required", field)
	}
}

func validatePromptTemplate(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("prompt_template required")
	}
	if len(prompt) > maxWorkerPromptTemplateBytes {
		return fmt.Errorf(
			"prompt_template max %d bytes (got %d)",
			maxWorkerPromptTemplateBytes, len(prompt),
		)
	}
	return nil
}

// JSON validators (validateSkillRefs, validateOutputChannelsJSON,
// validateParametersJSON, validateAllowlistJSON) live in
// validate_json.go to keep this file under the 300-line budget.

// validateCaps rejects negative caps. Zero means "runner default" for run
// caps and "no cap" for budget/failure-streak caps; positive values are
// passed through verbatim. We don't enforce upper bounds here because the
// runner owns operational defaults.
func validateCaps(
	inTokens, outTokens, toolCalls, wallSecs int,
	monthlyCostUSD float64, consecFailures int,
) error {
	negs := []struct {
		field string
		val   int
	}{
		{"max_input_tokens", inTokens},
		{"max_output_tokens", outTokens},
		{"max_tool_calls", toolCalls},
		{"max_wall_clock_seconds", wallSecs},
		{"max_consecutive_failures", consecFailures},
	}
	for _, n := range negs {
		if n.val < 0 {
			return fmt.Errorf("%s must be >= 0 (got %d)", n.field, n.val)
		}
	}
	if monthlyCostUSD < 0 {
		return fmt.Errorf("max_monthly_cost_usd must be >= 0 (got %v)", monthlyCostUSD)
	}
	return nil
}

func validateWorkspaceAccess(grants []store.WorkerWorkspaceAccess) error {
	seen := map[string]bool{}
	for i, g := range grants {
		wsID := strings.TrimSpace(g.WorkspaceID)
		if wsID == "" {
			return fmt.Errorf("workspace_access[%d].workspace_id required", i)
		}
		if seen[wsID] {
			return fmt.Errorf("workspace_access contains duplicate workspace_id %q", wsID)
		}
		seen[wsID] = true
		switch strings.TrimSpace(g.Access) {
		case store.WorkerWorkspaceAccessRead, store.WorkerWorkspaceAccessWrite:
			// valid
		default:
			return fmt.Errorf(
				"workspace_access[%d].access %q invalid (want read|write)",
				i, g.Access,
			)
		}
	}
	return nil
}

// validateModelProvider whitelists accepted providers and
// enforces the openai_compat → endpoint requirement. claude_cli accepts
// an optional endpoint that's interpreted as a binary path override
// (typically left blank to use `claude` from PATH).
//
// SECURITY (H4): claude_cli requires the daemon to be started with
// MCPLEXER_ALLOW_CLAUDE_CLI=1. Without the env, validation rejects the
// worker at create/update time so operators get a clear "opt-in
// required" error instead of a runtime exec failure once the schedule
// fires. Mirrors models.ErrClaudeCLINotAllowed — kept text-only here
// so admin doesn't have to import the models package.
func validateModelProvider(provider, endpoint string) error {
	switch provider {
	case providerAnthropic, providerOpenAI:
		return nil
	case providerClaudeCLI:
		if os.Getenv("MCPLEXER_ALLOW_CLAUDE_CLI") != "1" {
			return errors.New(
				"model_provider claude_cli requires MCPLEXER_ALLOW_CLAUDE_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	case providerOpenAICompat:
		if strings.TrimSpace(endpoint) == "" {
			return errors.New("model_endpoint_url required for openai_compat")
		}
		return nil
	case providerOpenCodeCLI:
		// SECURITY (H1): opencode_cli ALSO runs with NetworkHost (see
		// internal/models/opencode_cli.go:opencodeCLISandboxConfig), so
		// it carries the same egress-blast-radius risk as claude_cli.
		// Require the same explicit opt-in env at create/update time so
		// operators can't accidentally enable network-host subprocess
		// dispatch by just choosing opencode_cli from the UI. Mirrors
		// models.ErrOpenCodeCLINotAllowed; kept text-only here so admin
		// doesn't pull in the models package for one error constant.
		if os.Getenv("MCPLEXER_ALLOW_OPENCODE_CLI") != "1" {
			return errors.New(
				"model_provider opencode_cli requires MCPLEXER_ALLOW_OPENCODE_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	case providerGrokCLI:
		if os.Getenv("MCPLEXER_ALLOW_GROK_CLI") != "1" {
			return errors.New(
				"model_provider grok_cli requires MCPLEXER_ALLOW_GROK_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	case providerMiMoCLI:
		if os.Getenv("MCPLEXER_ALLOW_MIMO_CLI") != "1" {
			return errors.New(
				"model_provider mimo_cli requires MCPLEXER_ALLOW_MIMO_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	case providerGeminiCLI:
		if os.Getenv("MCPLEXER_ALLOW_GEMINI_CLI") != "1" {
			return errors.New(
				"model_provider gemini_cli requires MCPLEXER_ALLOW_GEMINI_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	case providerCodexCLI:
		if os.Getenv("MCPLEXER_ALLOW_CODEX_CLI") != "1" {
			return errors.New(
				"model_provider codex_cli requires MCPLEXER_ALLOW_CODEX_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	case providerPiCLI:
		if os.Getenv("MCPLEXER_ALLOW_PI_CLI") != "1" {
			return errors.New(
				"model_provider pi_cli requires MCPLEXER_ALLOW_PI_CLI=1 " +
					"(network-host subprocess opt-in until mcplexer-proxy UDS lands)",
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"model_provider %q invalid (want anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli|gemini_cli|codex_cli|pi_cli)",
			provider,
		)
	}
}

// validateExecMode whitelists "propose" and "autonomous".
func validateExecMode(mode string) error {
	switch mode {
	case "propose", "autonomous":
		return nil
	default:
		return fmt.Errorf("exec_mode %q invalid (want propose|autonomous)", mode)
	}
}

// validateConcurrencyPolicy whitelists "skip" and "queue".
func validateConcurrencyPolicy(policy string) error {
	switch policy {
	case "skip", "queue":
		return nil
	default:
		return fmt.Errorf("concurrency_policy %q invalid (want skip|queue)", policy)
	}
}

// applyUpdate copies every non-nil field from in onto w. Pointer-typed
// fields are the protocol for "this update touched this field"; nil
// means "leave it alone". Booleans and strings both use *T.
func applyUpdate(w *store.Worker, in UpdateInput) {
	if in.Name != nil {
		w.Name = *in.Name
	}
	if in.Description != nil {
		w.Description = *in.Description
	}
	if in.ModelProvider != nil {
		w.ModelProvider = *in.ModelProvider
	}
	if in.ModelID != nil {
		w.ModelID = *in.ModelID
	}
	if in.ModelEndpointURL != nil {
		w.ModelEndpointURL = *in.ModelEndpointURL
	}
	if in.SecretScopeID != nil {
		w.SecretScopeID = *in.SecretScopeID
	}
	if in.SkillName != nil {
		w.SkillName = *in.SkillName
		// Clearing skill_name implies clearing the legacy version pair
		// too — otherwise a Worker would persist a dangling
		// skill_version="latest" the runner ignores. Cosmetic but keeps
		// the row tidy.
		if strings.TrimSpace(w.SkillName) == "" {
			w.SkillVersion = ""
		}
	}
	if in.SkillVersion != nil {
		w.SkillVersion = *in.SkillVersion
	}
	if in.SkillRefs != nil {
		refs := *in.SkillRefs
		w.SkillRefs = refs
		// Mirror first entry into the legacy columns. Clearing the
		// slice clears the legacy columns too so the row stays
		// consistent under both lenses.
		if len(refs) == 0 {
			w.SkillName = ""
			w.SkillVersion = ""
		} else {
			w.SkillName = refs[0].Name
			w.SkillVersion = refs[0].Version
		}
	}
	if in.PromptTemplate != nil {
		w.PromptTemplate = *in.PromptTemplate
	}
	if in.ParametersJSON != nil {
		w.ParametersJSON = *in.ParametersJSON
	}
	if in.ScheduleSpec != nil {
		w.ScheduleSpec = *in.ScheduleSpec
	}
	if in.ToolAllowlistJSON != nil {
		w.ToolAllowlistJSON = *in.ToolAllowlistJSON
	}
	if in.CapabilityProfileJSON != nil {
		w.CapabilityProfileJSON = *in.CapabilityProfileJSON
	}
	if in.OutputChannelsJSON != nil {
		w.OutputChannelsJSON = *in.OutputChannelsJSON
	}
	if in.ExecMode != nil {
		w.ExecMode = *in.ExecMode
	}
	if in.ConcurrencyPolicy != nil {
		w.ConcurrencyPolicy = *in.ConcurrencyPolicy
	}
	if in.MemoryScopeID != nil {
		w.MemoryScopeID = *in.MemoryScopeID
	}
	if in.Enabled != nil {
		w.Enabled = *in.Enabled
	}
	if in.WorkspaceID != nil {
		w.WorkspaceID = *in.WorkspaceID
	}
	if in.WorkspaceAccess != nil {
		w.WorkspaceAccess = workerWorkspaceAccessOrDefault(w.WorkspaceID, *in.WorkspaceAccess)
	} else if in.WorkspaceID != nil {
		w.WorkspaceAccess = workerWorkspaceAccessOrDefault(w.WorkspaceID, w.WorkspaceAccess)
	}
	if in.MaxInputTokens != nil {
		w.MaxInputTokens = *in.MaxInputTokens
	}
	if in.MaxOutputTokens != nil {
		w.MaxOutputTokens = *in.MaxOutputTokens
	}
	if in.MaxToolCalls != nil {
		w.MaxToolCalls = *in.MaxToolCalls
	}
	if in.MaxWallClockSeconds != nil {
		w.MaxWallClockSeconds = *in.MaxWallClockSeconds
	}
	if in.MaxMonthlyCostUSD != nil {
		w.MaxMonthlyCostUSD = *in.MaxMonthlyCostUSD
	}
	if in.MaxConsecutiveFailures != nil {
		w.MaxConsecutiveFailures = *in.MaxConsecutiveFailures
	}
	if in.AutoPausedReason != nil {
		w.AutoPausedReason = *in.AutoPausedReason
	}
}
