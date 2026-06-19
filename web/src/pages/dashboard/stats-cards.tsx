import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import type {
  AuditStats,
  ErrorBreakdownEntry,
  ApprovalMetrics,
  CacheStats,
} from '@/api/types'
import { Activity, Database } from 'lucide-react'

export function ErrorBreakdownCard({ entries }: { entries: ErrorBreakdownEntry[] }) {
  if (entries.length === 0) return null
  const max = entries[0]?.count ?? 1
  return (
    <Card className="border-destructive/30">
      <CardHeader>
        <CardTitle className="text-sm font-medium uppercase tracking-wider text-destructive">
          Error Breakdown
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {entries.map((e) => {
          const blocked = e.error_type === 'blocked'
          return (
            <div key={`${e.group_key}-${e.error_type}`} className="space-y-1">
              <div className="flex items-center justify-between text-sm">
                <div className="flex items-center gap-2 min-w-0">
                  <span className="truncate min-w-0 font-mono" title={e.group_key}>{e.group_key}</span>
                  <Badge
                    variant="outline"
                    className={`shrink-0 text-[10px] px-1.5 py-0 ${
                      blocked
                        ? 'border-amber-500/40 text-amber-500'
                        : 'border-destructive/40 text-destructive'
                    }`}
                  >
                    {blocked ? 'blocked' : 'error'}
                  </Badge>
                </div>
                <span className={`font-mono ${blocked ? 'text-amber-500' : 'text-destructive'}`}>
                  {e.count}
                </span>
              </div>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
                <div
                  className={`h-full rounded-full ${blocked ? 'bg-amber-500/60' : 'bg-destructive/60'}`}
                  style={{ width: `${(e.count / max) * 100}%` }}
                />
              </div>
            </div>
          )
        })}
      </CardContent>
    </Card>
  )
}

export function ApprovalMetricsCard({ metrics }: { metrics: ApprovalMetrics }) {
  const total = metrics.pending_count + metrics.approved_count + metrics.denied_count + metrics.timed_out_count
  if (total === 0) return null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
          Approval Metrics
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <div className="text-xs text-muted-foreground">Pending</div>
            <div className="text-lg font-bold text-amber-400">{metrics.pending_count}</div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">Approved</div>
            <div className="text-lg font-bold text-chart-2">{metrics.approved_count}</div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">Denied</div>
            <div className="text-lg font-bold text-destructive">{metrics.denied_count}</div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">Timed Out</div>
            <div className="text-lg font-bold text-muted-foreground">{metrics.timed_out_count}</div>
          </div>
        </div>
        {metrics.avg_wait_ms > 0 && (
          <div className="mt-3 border-t border-border/50 pt-3 text-xs text-muted-foreground">
            Avg. wait: <span className="font-mono">{(metrics.avg_wait_ms / 1000).toFixed(1)}s</span>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

export function CacheStatsCard({ stats, auditStats }: { stats: CacheStats; auditStats: AuditStats | null }) {
  const tc = stats.tool_call
  const tcReqs = tc.hits + tc.misses
  const cacheRate = tcReqs > 0 ? ((tc.hits / tcReqs) * 100).toFixed(1) : '0.0'

  const allowed = auditStats ? auditStats.success_count : 0
  const blocked = auditStats ? auditStats.blocked_count : 0
  const errors = auditStats ? auditStats.error_count : 0

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
            Cache & Routing
          </CardTitle>
          <span className="text-xs font-mono text-muted-foreground">{cacheRate}% cache hit rate</span>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Database className="h-3 w-3" />
            <span className="uppercase tracking-wider">Tool Call Cache</span>
          </div>
          <div className="grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
            <div>
              <div className="text-muted-foreground">Hits</div>
              <div className="font-mono font-medium text-chart-2">{tc.hits}</div>
            </div>
            <div>
              <div className="text-muted-foreground">Misses</div>
              <div className="font-mono font-medium">{tc.misses}</div>
            </div>
            <div>
              <div className="text-muted-foreground">Entries</div>
              <div className="font-mono font-medium">{tc.entries}</div>
            </div>
            <div>
              <div className="text-muted-foreground">Hit %</div>
              <div className="font-mono font-medium text-chart-2">
                {tc.hit_rate > 0 ? `${(tc.hit_rate * 100).toFixed(0)}%` : '-'}
              </div>
            </div>
          </div>
        </div>
        <div className="space-y-2 border-t border-border/50 pt-3">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Activity className="h-3 w-3" />
            <span className="uppercase tracking-wider">Route Decisions</span>
          </div>
          <div className="grid grid-cols-3 gap-2 text-xs">
            <div>
              <div className="text-muted-foreground">Allowed</div>
              <div className="font-mono font-medium text-chart-2">{allowed}</div>
            </div>
            <div>
              <div className="text-muted-foreground">Blocked</div>
              <div className="font-mono font-medium text-amber-400">{blocked}</div>
            </div>
            <div>
              <div className="text-muted-foreground">Errors</div>
              <div className="font-mono font-medium text-destructive">{errors}</div>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
