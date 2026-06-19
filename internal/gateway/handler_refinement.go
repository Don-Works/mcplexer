package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// suggestDescriptionToolDef returns the builtin tool definition for description refinement.
// This tool is not in the static tools/list — it's discoverable through search_tools.
func suggestDescriptionToolDef() Tool {
	extras := withAnnotations(ToolAnnotations{
		Title:           "Suggest Description Improvement",
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(false),
		OpenWorldHint:   boolPtr(false),
	})
	extras["x-search-tags"] = json.RawMessage(`"description,improve,suggest,refine,feedback,tool description"`)

	return Tool{
		Name:        "mcpx__suggest_description",
		Description: "Suggest an improved description for a tool you have used. Provide the full namespaced tool name, the improved description, and a rationale explaining what you changed and why. Good suggestions clarify ambiguous behavior, add missing context, or correct inaccuracies you discovered while using the tool. Call this tool directly (not through execute_code).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tool_name": {
					"type": "string",
					"description": "The full namespaced tool name (e.g. github__create_issue)"
				},
				"description": {
					"type": "string",
					"description": "The improved tool description text"
				},
				"rationale": {
					"type": "string",
					"description": "Explain what you changed and why — what was unclear, missing, or incorrect"
				}
			},
			"required": ["tool_name", "description"]
		}`),
		Extras: extras,
	}
}

// searchableBuiltins returns builtin tools that should appear in search
// results but not in the static tools/list response.
//
// Two sources contribute:
//  1. The description-refinement tool (mcpx__suggest_description) — always
//     hidden from tools/list by design, gated by refinement mode setting.
//  2. When slim-surface mode is on (default), every built-in *except*
//     the keep-list (see slimSurfaceKeepers in schema.go) is exposed here
//     so mcpx__search_tools can still find them.
func (h *handler) searchableBuiltins(ctx context.Context) []Tool {
	var out []Tool

	if h.refinementMode(ctx) != "off" {
		out = append(out, suggestDescriptionToolDef())
	}

	if h.slimSurfaceEnabled(ctx) {
		full := h.buildAllBuiltinTools(ctx)
		for _, t := range full {
			if isSlimSurfaceKeeper(t.Name) {
				continue
			}
			out = append(out, t)
		}
	}

	return out
}

// handleSuggestDescription processes a model's suggestion for an improved tool description.
func (h *handler) handleSuggestDescription(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	var p struct {
		ToolName    string `json:"tool_name"`
		Description string `json:"description"`
		Rationale   string `json:"rationale"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	p.ToolName = strings.TrimSpace(p.ToolName)
	p.Description = strings.TrimSpace(p.Description)

	if p.ToolName == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "tool_name is required"}
	}
	if p.Description == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "description is required"}
	}

	mode := h.refinementMode(ctx)
	if mode == "off" {
		return marshalErrorResult("Description refinement is disabled."), nil
	}

	// Dedup: one pending suggestion per tool per session.
	has, err := h.store.HasPendingForToolBySession(ctx, p.ToolName, h.sessions.sessionID())
	if err != nil {
		slog.Warn("check pending description", "error", err)
	}
	if has {
		return marshalErrorResult(
			fmt.Sprintf("You already have a pending suggestion for %s.", p.ToolName),
		), nil
	}

	// Capture the original description if this is the first suggestion for this tool.
	if err := h.ensureOriginalCaptured(ctx, p.ToolName); err != nil {
		slog.Warn("capture original description", "error", err)
	}

	v := &store.ToolDescriptionVersion{
		ToolName:    p.ToolName,
		Description: p.Description,
		Source:      "model",
		Status:      "pending",
		SessionID:   h.sessions.sessionID(),
		Model:       h.sessions.modelHint(),
		WorkspaceID: h.currentWorkspaceID(ctx),
		Rationale:   strings.TrimSpace(p.Rationale),
	}
	if rpc := h.requireWorkspaceWrite(ctx, v.WorkspaceID); rpc != nil {
		return nil, rpc
	}

	if mode == "auto" {
		v.Status = "active"
		v.ReviewedBy = "auto"
	}

	if err := h.store.CreateToolDescriptionVersion(ctx, v); err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	// If auto-accept, supersede any previously active version.
	if mode == "auto" {
		if err := h.store.ActivateVersion(ctx, v.ID, "auto", "auto-accepted"); err != nil {
			slog.Warn("auto-activate description", "error", err)
		}
		h.invalidateRefinedDescriptions()
		return marshalToolResult(
			fmt.Sprintf("Description for %s updated (auto-accepted).", p.ToolName),
		), nil
	}

	return marshalToolResult(
		fmt.Sprintf("Suggestion for %s submitted for review. Thank you!", p.ToolName),
	), nil
}

// ensureOriginalCaptured stores the original description as version 1 if none exists.
func (h *handler) ensureOriginalCaptured(ctx context.Context, toolName string) error {
	existing, _, err := h.store.ListToolDescriptionVersions(ctx, store.ToolDescriptionFilter{
		ToolName: &toolName,
		Limit:    1,
	})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil // already have versions for this tool
	}

	origDesc := h.findToolDescription(ctx, toolName)
	if origDesc == "" {
		return nil // can't capture what we don't have
	}

	v := &store.ToolDescriptionVersion{
		ToolName:    toolName,
		Description: origDesc,
		Source:      "original",
		Status:      "active",
	}
	return h.store.CreateToolDescriptionVersion(ctx, v)
}

// findToolDescription looks up the current description for a namespaced tool.
func (h *handler) findToolDescription(ctx context.Context, toolName string) string {
	tools, err := h.gatherCodeModeTools(ctx)
	if err != nil {
		slog.Warn("gather tools for description lookup", "error", err)
		return ""
	}
	for _, t := range tools {
		if t.Name == toolName {
			return t.Description
		}
	}
	return ""
}

// refinementMode returns the current description refinement mode from settings.
func (h *handler) refinementMode(ctx context.Context) string {
	if h.settingsSvc == nil {
		return "off"
	}
	mode := h.settingsSvc.Load(ctx).DescriptionRefinementMode
	if mode == "" {
		return "manual"
	}
	return mode
}
