package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// spawnSubagentInput is the decoded payload for mcplexer__spawn_subagent.
type spawnSubagentInput struct {
	WorkspaceID         string `json:"workspace_id"`
	Name                string `json:"name"`
	Prompt              string `json:"prompt"`
	ModelProvider       string `json:"model_provider"`
	ModelID             string `json:"model_id"`
	ModelEndpointURL    string `json:"model_endpoint_url,omitempty"`
	SecretScopeID       string `json:"secret_scope_id"`
	ReplyToTrigger      *bool  `json:"reply_to_trigger,omitempty"`
	MaxWallClockSeconds int    `json:"max_wall_clock_seconds,omitempty"`
	MaxToolCalls        int    `json:"max_tool_calls,omitempty"`
	ToolAllowlistJSON   string `json:"tool_allowlist_json,omitempty"`
	TriggerMessageID    string `json:"trigger_message_id,omitempty"`
}

// spawnSubagentOutput is the response shape. RunID is empty in the
// dispatched-async path because the call returns before the run is
// constructed; callers can locate the run via list_worker_runs once
// the goroutine starts it.
type spawnSubagentOutput struct {
	WorkerID string `json:"worker_id"`
	RunID    string `json:"run_id,omitempty"`
	Status   string `json:"status"`
}

// defaultSubagentAllowlist is the toolset we give a sub-agent when the
// caller doesn't pin its own. Mesh + memory + task primitives + code
// mode + skill search — enough to do real work, no admin tools (a
// sub-agent that can rewrite worker rows would be a privilege-
// escalation foothold).
//
// SECURITY (M3): secret__list_refs was previously in this list, which
// meant every spawned sub-agent could enumerate the global secret
// namespace by default. Refs are names not values (the substitution
// gate at dispatch time is the actual access control), but
// enumeration is reconnaissance — a sub-agent that learns the names
// "stripe_prod", "github_enterprise", "aws_root" without needing any
// scope grant is doing pre-attack discovery. Removed from the default;
// callers that legitimately need it (e.g. an inventory agent) can
// opt in via the tool_allowlist_json parameter.
const defaultSubagentAllowlist = `["mcpx__execute_code","mcpx__search_tools","mcpx__skill_search","mcpx__skill_get","mesh__send","mesh__receive","mesh__list_peers","mesh__list_agents","memory__save","memory__recall","memory__list","task__create","task__get","task__list","task__update","task__append_note"]`

// handleSpawnSubagent implements mcplexer__spawn_subagent. Validates,
// creates a one-shot Worker via the admin service, fires its first run
// via the runner, returns {worker_id, run_id, status}.
//
// Trigger inheritance: when the call originates from an in-process
// worker run (e.g. the Telegram concierge dispatching heavier work),
// we read the parent's trigger_message_id from ctx and use it as the
// fallback for in.TriggerMessageID. Without this, the sub-agent's
// reply_to_trigger output channel has no ReplyTo set and the reply
// broadcasts to the workspace instead of threading back to the human
// who asked the question.
//
// Scope auto-fill: CLI providers ignore the
// secret_scope_id at runtime (those providers read host-installed
// creds), but the workers schema declares it NOT NULL. When the
// caller omits it for one of those providers, we silently substitute
// the first available scope so the calling LLM doesn't have to know
// which scope ID exists on this box.
func (b *InternalBackend) handleSpawnSubagent(
	ctx context.Context, args json.RawMessage,
) json.RawMessage {
	svc := b.workerSvc
	if svc == nil {
		return errorResult("worker admin service not available — daemon built without it")
	}
	var in spawnSubagentInput
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	// Inherit the parent run's trigger_message_id when the caller
	// omits it. Done before validation so a missing trigger context
	// degrades to "no reply threading" rather than a hard error.
	if in.TriggerMessageID == "" {
		if parent, ok := runner.WorkerRunCtxFromContext(ctx); ok && parent.TriggerMessageID != "" {
			in.TriggerMessageID = parent.TriggerMessageID
		}
	}
	// Auto-fill secret_scope_id for CLI providers that don't use it.
	// Done before validation so "missing required fields" stops
	// firing on the common-path concierge call.
	if in.SecretScopeID == "" && providerIgnoresScope(in.ModelProvider) {
		if id := firstAvailableScopeID(ctx, b.store); id != "" {
			in.SecretScopeID = id
		}
	}
	if err := validateSpawnSubagentInput(in); err != nil {
		return errorResult(err.Error())
	}

	// Auto-fill model_endpoint_url from the matching worker_model_profile
	// when the caller omits it — saves the concierge from having to know
	// the absolute path of opencode / claude on this box.
	if in.ModelEndpointURL == "" {
		if url := lookupProfileEndpoint(ctx, b.store, in.ModelProvider); url != "" {
			in.ModelEndpointURL = url
		}
	}

	w, err := svc.Create(ctx, buildSubagentCreateInput(in))
	if err != nil {
		return mapWorkerErr(err)
	}
	// Fire the run in a goroutine with a background context so the
	// concierge (or whoever called us) returns immediately and the
	// sub-agent's wall-clock isn't bounded by the caller's. The new
	// ctx has its own timeout matched to the worker's configured cap +
	// 60s headroom so a hung run still gets cleaned up.
	timeout := time.Duration(w.MaxWallClockSeconds+60) * time.Second
	if w.MaxWallClockSeconds <= 0 {
		timeout = 10 * time.Minute
	}
	// Carry the parent's trigger info into the detached run so the
	// sub-agent's reply_to_trigger mesh emission chains to the right
	// message. Empty TriggerMessageID falls back to a manual run with
	// no reply threading (matches pre-fix behaviour).
	opts := runner.RunOpts{}
	if in.TriggerMessageID != "" {
		opts.TriggerKind = "mesh"
		opts.TriggerMessageID = in.TriggerMessageID
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if _, runErr := svc.RunNowWithOpts(runCtx, w.ID, opts); runErr != nil {
			slog.Warn("spawn_subagent: detached RunNow failed",
				"worker_id", w.ID, "error", runErr)
		}
	}()
	// We don't have the run_id yet (RunNow creates it inside). The
	// caller can locate it via mcplexer__list_worker_runs(worker_id).
	return mustJSONResult(spawnSubagentOutput{
		WorkerID: w.ID,
		RunID:    "",
		Status:   "dispatched",
	})
}

// providerIgnoresScope reports whether the worker's secret_scope_id is
// effectively a placeholder for this provider. claude_cli, opencode_cli,
// grok_cli, and mimo_cli read host-installed credentials, so the
// scope is unused at runtime — the workers schema just needs SOME
// value to satisfy NOT NULL.
func providerIgnoresScope(provider string) bool {
	switch provider {
	case "claude_cli", "opencode_cli", "grok_cli", "mimo_cli", "gemini_cli", "codex_cli", "pi_cli":
		return true
	}
	return false
}

// firstAvailableScopeID returns any existing auth_scope id, preferring
// short stable-name ids over UUIDs (so audits read "aikido-client-…"
// rather than "8b4f64b8-…"). Empty string when no scopes exist or
// the store call fails — caller treats both as "leave blank, let
// validation surface the error".
func firstAvailableScopeID(ctx context.Context, st store.Store) string {
	if st == nil {
		return ""
	}
	scopes, err := st.ListAuthScopes(ctx)
	if err != nil || len(scopes) == 0 {
		return ""
	}
	// Prefer human-named scope ids (contain a dash but not a UUID
	// pattern) over UUIDs. Falls back to the first scope when none
	// are name-like.
	for _, sc := range scopes {
		if !isUUIDLike(sc.ID) {
			return sc.ID
		}
	}
	return scopes[0].ID
}

// isUUIDLike reports whether id looks like a UUID (8-4-4-4-12 hex).
// Used to prefer human-readable scope ids when auto-filling.
func isUUIDLike(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

func validateSpawnSubagentInput(in spawnSubagentInput) error {
	missing := []string{}
	if in.WorkspaceID == "" {
		missing = append(missing, "workspace_id")
	}
	if in.Name == "" {
		missing = append(missing, "name")
	}
	if in.Prompt == "" {
		missing = append(missing, "prompt")
	}
	if in.ModelProvider == "" {
		missing = append(missing, "model_provider")
	}
	if in.ModelID == "" {
		missing = append(missing, "model_id")
	}
	if in.SecretScopeID == "" {
		missing = append(missing, "secret_scope_id")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %v", missing)
	}
	return nil
}

// buildSubagentCreateInput translates the spawn payload into the
// admin.CreateInput the workers service expects. Defaults sized for
// the "sub-agent doing real work" case rather than the chat-loop case.
func buildSubagentCreateInput(in spawnSubagentInput) admin.CreateInput {
	wall := in.MaxWallClockSeconds
	if wall == 0 {
		wall = 600
	}
	tools := in.MaxToolCalls
	if tools == 0 {
		tools = 80
	}
	allowlist := in.ToolAllowlistJSON
	if allowlist == "" {
		allowlist = defaultSubagentAllowlist
	}
	reply := true
	if in.ReplyToTrigger != nil {
		reply = *in.ReplyToTrigger
	}
	channels := buildSubagentOutputChannels(reply)

	enabled := true
	return admin.CreateInput{
		Name:                in.Name,
		Description:         "Auto-spawned sub-agent (mcplexer__spawn_subagent).",
		ModelProvider:       in.ModelProvider,
		ModelID:             in.ModelID,
		ModelEndpointURL:    in.ModelEndpointURL,
		SecretScopeID:       in.SecretScopeID,
		PromptTemplate:      in.Prompt,
		ParametersJSON:      "{}",
		ScheduleSpec:        "manual",
		ToolAllowlistJSON:   allowlist,
		OutputChannelsJSON:  channels,
		ExecMode:            runner.ExecModeAutonomous,
		ConcurrencyPolicy:   "skip",
		Enabled:             &enabled,
		WorkspaceID:         in.WorkspaceID,
		MaxWallClockSeconds: wall,
		MaxToolCalls:        tools,
	}
}

// lookupProfileEndpoint returns the endpoint URL of the first model
// profile whose provider matches. Empty string when none match or
// the store call fails — caller treats both as "leave blank".
func lookupProfileEndpoint(ctx context.Context, st store.Store, provider string) string {
	if st == nil || provider == "" {
		return ""
	}
	profiles, err := st.ListModelProfiles(ctx)
	if err != nil {
		return ""
	}
	for _, p := range profiles {
		if p.Provider == provider && p.EndpointURL != "" {
			return p.EndpointURL
		}
	}
	return ""
}

// buildSubagentOutputChannels returns the OutputChannelsJSON for a
// sub-agent. When replyToTrigger is true the mesh sink threads its
// reply back to the inbound message id (so a Telegram concierge sees
// the result land on the same thread). When false it broadcasts to the
// workspace at normal priority.
func buildSubagentOutputChannels(replyToTrigger bool) string {
	type channel struct {
		Type           string `json:"type"`
		Priority       string `json:"priority"`
		Tags           string `json:"tags,omitempty"`
		NotifyUser     bool   `json:"notify_user,omitempty"`
		ReplyToTrigger bool   `json:"reply_to_trigger,omitempty"`
	}
	ch := channel{
		Type:           "mesh",
		Priority:       "high",
		Tags:           "subagent_reply",
		NotifyUser:     false,
		ReplyToTrigger: replyToTrigger,
	}
	b, _ := json.Marshal([]channel{ch})
	return string(b)
}
