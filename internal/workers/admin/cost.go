// Package admin — cost.go (M2) exposes the workspace-wide cost
// dashboard rollup so the HTTP layer + MCP tool surface stay in
// lockstep. The aggregation itself lives in store.WorkerStore; this
// file is a thin defaulting + validation pass.
package admin

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// CostAggregateInput narrows the dashboard query. Empty WorkspaceID
// means "every workspace this caller can see" — the higher-level CWD
// gate already restricts the surface to admin contexts.
type CostAggregateInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Days        int    `json:"days,omitempty"`
}

// CostAggregateOutput is the JSON payload the dashboard renders. The
// workspace-wide totals are derived here so every consumer agrees on
// the headline number.
type CostAggregateOutput struct {
	Days           int                         `json:"days"`
	WorkspaceID    string                      `json:"workspace_id,omitempty"`
	TotalMTDUSD    float64                     `json:"total_mtd_usd"`
	TotalWindowUSD float64                     `json:"total_window_usd"`
	TotalRuns30D   int                         `json:"total_runs_30d"`
	Workers        []store.WorkerCostAggregate `json:"workers"`
}

// CostAggregate returns per-worker cost rollups + workspace-wide
// totals. Defaults Days to 30 when zero. The DB layer caps Days at
// 365 to prevent runaway grids.
func (s *Service) CostAggregate(
	ctx context.Context, in CostAggregateInput,
) (CostAggregateOutput, error) {
	days := in.Days
	if days <= 0 {
		days = 30
	}
	rows, err := s.store.WorkerCostAggregate(ctx, in.WorkspaceID, days, s.clock.Now())
	if err != nil {
		return CostAggregateOutput{}, err
	}
	if rows == nil {
		rows = []store.WorkerCostAggregate{}
	}
	out := CostAggregateOutput{
		Days:        days,
		WorkspaceID: in.WorkspaceID,
		Workers:     rows,
	}
	for _, row := range rows {
		out.TotalMTDUSD += row.MonthToDateUSD
		out.TotalRuns30D += row.RunCount30D
		for _, d := range row.DailyCosts {
			out.TotalWindowUSD += d.CostUSD
		}
	}
	return out, nil
}
