// TrendsSection — the previous dashboard, demoted to a collapsible
// "below the fold" details block. Charts, leaderboards, server health,
// cache stats, error breakdown — everything you might want once the
// at-a-glance row has done its job.

import type { DashboardData, SessionInfo } from '@/api/types'
import { ErrorBreakdownCard, ApprovalMetricsCard, CacheStatsCard } from './stats-cards'
import { ToolLeaderboardTable } from './leaderboard-table'
import { ServerHealthCards } from './server-health'
import { ActiveSessionsTable } from './sessions-table'
import { RouteHitMapTable } from './route-hits-table'
import { ServerPerformancePanel } from './server-timings'

interface Props {
  data: DashboardData
  sessions: SessionInfo[]
  wsName: (id: string) => string
  // asName is kept on the API for parity with the audit dialog, even
  // though no current trends panel uses it. Removing it would only
  // force callers to thread it again the next time a trend panel
  // needs auth-scope naming.
  asName: (id: string) => string
}

export function TrendsSection({ data, sessions, wsName }: Props) {
  const hasApprovalMetrics =
    data.approval_metrics &&
    data.approval_metrics.pending_count +
      data.approval_metrics.approved_count +
      data.approval_metrics.denied_count +
      data.approval_metrics.timed_out_count >
      0
  const hasErrorBreakdown = (data.error_breakdown ?? []).length > 0

  return (
    <details
      data-testid="dash-trends"
      className="group border border-border bg-card/20"
    >
      <summary className="flex cursor-pointer items-center justify-between border-b border-border/60 px-4 py-2 hover:bg-accent/15">
        <span className="text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          Trends, throughput &amp; server health
        </span>
        <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60 group-open:hidden">
          show
        </span>
        <span className="hidden font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60 group-open:inline">
          hide
        </span>
      </summary>
      <div className="space-y-4 p-4">
        <ServerPerformancePanel timings={data.server_timings ?? []} />

        <div className="grid gap-4 lg:grid-cols-2">
          <ActiveSessionsTable sessions={sessions} wsName={wsName} />
          <CacheStatsCard
            stats={
              data.cache_stats ?? {
                tool_call: { hits: 0, misses: 0, evictions: 0, entries: 0, hit_rate: 0 },
                route_resolution: { hits: 0, misses: 0, evictions: 0, entries: 0, hit_rate: 0 },
              }
            }
            auditStats={data.stats}
          />
        </div>

        <div className={`grid gap-4 ${hasErrorBreakdown ? 'lg:grid-cols-2' : ''}`}>
          <ToolLeaderboardTable entries={data.tool_leaderboard ?? []} />
          {hasErrorBreakdown && <ErrorBreakdownCard entries={data.error_breakdown ?? []} />}
        </div>

        <div className="grid gap-4 lg:grid-cols-2">
          <ServerHealthCards
            entries={data.server_health ?? []}
            downstreams={data.active_downstreams ?? []}
          />
          <div className="space-y-4">
            <RouteHitMapTable entries={data.route_hit_map ?? []} />
            {hasApprovalMetrics && data.approval_metrics && (
              <ApprovalMetricsCard metrics={data.approval_metrics} />
            )}
          </div>
        </div>
      </div>
    </details>
  )
}
