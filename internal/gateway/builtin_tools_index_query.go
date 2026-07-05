package gateway

import "encoding/json"

// indexQueryToolDefinitions returns the read-side half of the index__* surface:
// tests_for, summary, recent_changes, map_failure, context. Concatenated into
// the full set by indexToolDefinitions.
func indexQueryToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "index__tests_for",
			Description: "Find the tests that own a source file (Go: same-package _test.go by naming; TS: .test./.spec. files by naming and by import edges), with confidence levels. Use before changing a file to know what to run. For the reverse blast-radius question (who imports this file) use index__deps direction=importers.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "Root-relative source file path. Required."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["file"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Find Owning Tests",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__summary",
			Description: "Heuristic one-file summary without reading the file: package/module doc, exported symbols with signatures, line/import/importer counts, owning tests. Cheaper than reading the file when you only need orientation on ONE known file. For a whole-task, multi-file pack use index__context instead.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "Root-relative file path. Required."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["file"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Summarize File",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__recent_changes",
			Description: "Recent git history for the repo or a path: commits (hash, author, date, subject, files) plus per-file churn counts. Live from git log — no index__build needed. Useful for 'what changed here lately' before editing.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Restrict to a root-relative file or directory. Omit for the whole repo."},
					"days": {"type": "integer", "description": "Look-back window in days. Default 14."},
					"limit": {"type": "integer", "description": "Max commits. Default 20, max 100."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Recent Changes",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__map_failure",
			Description: "Paste a test failure, panic, or stack trace (Go test output, vitest/jest output); returns ranked candidate files to look at, with reasons (path mentioned, stack frame, failing test ownership, symbol match). Start debugging here instead of grepping. For a plain name lookup use index__symbols; for a task-scoped pack use index__context.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"text": {"type": "string", "description": "The raw failure / panic / stack-trace text. Required."},
					"limit": {"type": "integer", "description": "Max candidate files. Default 10."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["text"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Map Failure to Files",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "index__context",
			Description: "THE context-pack call: given a task description or question, returns a token-budgeted, ranked pack of the right files — summaries, key symbols with line numbers, owning tests, recent commits — instead of you slurping the repo. Auto-refreshes the index if git HEAD moved. Designed for small-context models: ask first, read files second. For one known file use index__summary; for a failure use index__map_failure.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Task description or question to select files for. Required."},
					"budget_tokens": {"type": "integer", "description": "Token budget for the pack. Default 4000, max 16000."},
					"workspace_id": {"type": "string", "description": "Override current workspace."}
				},
				"required": ["query"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Task Context Pack",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
