// tools_workers_extras.go (M0.7) — supplementary MCP admin tools that
// round out the Workers surface so an agent driving mcplexer purely
// over MCP can answer "how much have my workers spent this month?" and
// "what tools can I add to a worker's allowlist?" without dropping to
// the HTTP API.
//
// Kept in a separate file from tools_workers.go to honour the
// 300-line-per-file budget — tools_workers.go is at its cap.
package control

import (
	"github.com/don-works/mcplexer/internal/gateway"
)

// workerCostAggregateToolDef declares mcplexer__worker_cost_aggregate.
// Mirrors GET /api/v1/workers/cost-aggregate so MCP-only admins can
// roll up monthly + windowed worker spend in one call. Returns the
// same payload shape (admin.CostAggregateOutput) the PWA cost
// dashboard renders.
func workerCostAggregateToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "worker_cost_aggregate",
		Description: "Aggregate worker spend across a date window. Returns workspace + per-worker month-to-date totals, daily-bucket sparklines, and 30-day run counts. Mirrors the workspace cost dashboard. Optional workspace_id narrows the rollup; omit to scan every workspace. Optional days defaults to 30 (max 365).",
		InputSchema: schema(props{
			"workspace_id": propStr("Optional: limit aggregation to one workspace. Omit to scan all."),
			"days":         propInt("Optional: window size in days (default 30, max 365)."),
		}, nil),
	}
}

// listAvailableToolsToolDef declares mcplexer__list_available_tools.
// Returns every tool currently advertised by every registered
// downstream MCP server, projected into the same {name, description,
// namespace, write_class} shape /api/v1/tools serves. Lets an MCP-only
// admin build a Worker's tool_allowlist_json without first having to
// know what's reachable.
func listAvailableToolsToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_available_tools",
		Description: "List every tool currently advertised by every registered downstream MCP server. Returns one row per (name, description, namespace, write_class). write_class flags tools that look side-effecting by name — propose-mode workers gate on this same heuristic. Use to build a Worker's tool_allowlist_json without guessing tool names.",
		InputSchema: schema(nil, nil),
	}
}
