// handlers_memory_import.go — backend-side dispatch for the
// mcplexer__memory_import_claude_cli admin tool. Ingests the user's
// existing Claude Code auto-memory (~/.claude/projects/*/memory/*.md)
// into the mcplexer memory store. CWD-gated to ~/.mcplexer like every
// other mcplexer__* admin tool.
package control

import (
	"context"
	"encoding/json"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/memory/claudecli"
)

// memoryImportClaudeCliToolDef declares
// mcplexer__memory_import_claude_cli. Idempotent — re-running over the
// same files skips rows we already imported (deterministic ID derived
// from sha256(path + content)).
func memoryImportClaudeCliToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "memory_import_claude_cli",
		Description: "Import the user's existing Claude Code auto-memory files (~/.claude/projects/*/memory/*.md) into the mcplexer memory store. One-shot warm start. Idempotent — re-running over the same files is a no-op for unchanged content. The importer is READ-ONLY on ~/.claude/. Each file becomes a note-kind memory record, tagged with claude-cli + claude-cli-<type>, with the originSessionId carried into source_session_id for forensic redaction.",
		InputSchema: schema(props{
			"base_dir": propStr(
				"Optional override for the scan root. Default: ~/.claude/projects."),
			"workspace_id": propStr(
				"Optional workspace ID to scope every imported memory to. Empty = global."),
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "Report what would be imported without writing.",
			},
		}, nil),
	}
}

// handleMemoryImportClaudeCli serves
// mcplexer__memory_import_claude_cli. Requires the InternalBackend to
// have a non-nil store (defence-in-depth — the daemon always wires one).
func (b *InternalBackend) handleMemoryImportClaudeCli(
	ctx context.Context, args json.RawMessage,
) json.RawMessage {
	if b.store == nil {
		return errorResult("memory store not wired")
	}
	var in struct {
		BaseDir     string `json:"base_dir,omitempty"`
		WorkspaceID string `json:"workspace_id,omitempty"`
		DryRun      bool   `json:"dry_run,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errorResult("invalid params: " + err.Error())
		}
	}
	opts := claudecli.ImportOptions{BaseDir: in.BaseDir, DryRun: in.DryRun}
	if in.WorkspaceID != "" {
		ws := in.WorkspaceID
		opts.WorkspaceID = &ws
	}
	res, err := claudecli.Import(ctx, b.store, opts)
	if err != nil {
		return errorResult(err.Error())
	}
	return mustJSONResult(res)
}
