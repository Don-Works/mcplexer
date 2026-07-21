package admin_test

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestAnnotateToolCallsCap(t *testing.T) {
	worker := &store.Worker{MaxToolCalls: 80}
	cases := []struct {
		name     string
		run      store.WorkerRun
		exceeded bool
		scope    string
	}{
		{
			name: "cli audit exceeded",
			run: store.WorkerRun{
				ModelProvider:  "grok_cli",
				ToolCallsCount: 120,
				Status:         "success",
			},
			exceeded: true,
			scope:    "cli_audit",
		},
		{
			name: "cli audit within cap",
			run: store.WorkerRun{
				ModelProvider:  "opencode_cli",
				ToolCallsCount: 12,
				Status:         "success",
			},
			exceeded: false,
			scope:    "cli_audit",
		},
		{
			name: "gateway cap exceeded via status",
			run: store.WorkerRun{
				ModelProvider:  "anthropic",
				ToolCallsCount: 81,
				Status:         "cap_exceeded",
				Error:          "max tool calls (80) exceeded",
			},
			exceeded: true,
			scope:    "gateway_loop",
		},
		{
			name: "gateway within cap",
			run: store.WorkerRun{
				ModelProvider:  "anthropic",
				ToolCallsCount: 5,
				Status:         "success",
			},
			exceeded: false,
			scope:    "gateway_loop",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.run
			admin.AnnotateToolCallsCapForTest(&r, worker)
			if r.ToolCallsCapExceeded != tc.exceeded {
				t.Fatalf("exceeded = %v, want %v", r.ToolCallsCapExceeded, tc.exceeded)
			}
			if r.ToolCallsCapScope != tc.scope {
				t.Fatalf("scope = %q, want %q", r.ToolCallsCapScope, tc.scope)
			}
		})
	}
}
