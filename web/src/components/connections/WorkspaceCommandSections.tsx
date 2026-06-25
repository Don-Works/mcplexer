import { Link } from 'react-router-dom'
import { Activity } from 'lucide-react'

import type { AuditRecord } from '@/api/types'
import type { MemoryStats } from '@/api/memory'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { CommandTone } from './WorkspaceActionFeed'

export function MetricStrip({
  items,
}: {
  items: Array<{ icon: React.ReactNode; label: string; value: string; detail: string; tone: CommandTone }>
}) {
  return (
    <div className="grid grid-cols-1 border border-border/50 sm:grid-cols-2 xl:grid-cols-4">
      {items.map((item, index) => (
        <div
          key={item.label}
          className={cn('min-w-0 p-3', index > 0 && 'border-t border-border/50 sm:border-l sm:border-t-0')}
        >
          <div className="flex items-center justify-between gap-2">
            <span className="flex min-w-0 items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              {item.icon}
              <span className="truncate">{item.label}</span>
            </span>
            <Badge variant="outline" tone={item.tone}>{item.value}</Badge>
          </div>
          <div className="mt-2 truncate text-xs text-muted-foreground">{item.detail}</div>
        </div>
      ))}
    </div>
  )
}

export function ScopeMap({
  routeCount,
  protectedRouteCount,
  openTaskCount,
  urgentTaskCount,
  delegationReviewCount,
  runningDelegationCount,
}: {
  routeCount: number
  protectedRouteCount: number
  openTaskCount: number
  urgentTaskCount: number
  delegationReviewCount: number
  runningDelegationCount: number
}) {
  const rows = [
    ['Access boundary', `${routeCount} access rule${routeCount === 1 ? '' : 's'}`, protectedRouteCount > 0 ? `${protectedRouteCount} gated` : 'direct'],
    ['Agent work', `${openTaskCount} open task${openTaskCount === 1 ? '' : 's'}`, urgentTaskCount > 0 ? `${urgentTaskCount} high priority` : 'normal'],
    ['Delegation lane', `${runningDelegationCount} running`, delegationReviewCount > 0 ? `${delegationReviewCount} review` : 'reviewed'],
  ]

  return (
    <div className="border border-border/50">
      <div className="border-b border-border/50 px-3 py-2 text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
        Workspace scope
      </div>
      <div className="divide-y divide-border/40">
        {rows.map(([label, primary, secondary]) => (
          <div key={label} className="grid grid-cols-[8.5rem_1fr_auto] items-center gap-3 px-3 py-2 text-sm">
            <span className="text-muted-foreground">{label}</span>
            <span className="min-w-0 truncate">{primary}</span>
            <span className="font-mono text-[11px] text-muted-foreground/70">{secondary}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

export function RecentAudit({ rows }: { rows: AuditRecord[] }) {
  return (
    <div className="border border-border/50">
      <div className="flex items-center justify-between gap-3 border-b border-border/50 px-3 py-2">
        <h3 className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
          Recent tool calls
        </h3>
        <Activity className="h-3.5 w-3.5 text-muted-foreground" />
      </div>
      {rows.length === 0 ? (
        <p className="px-3 py-4 text-sm text-muted-foreground">No recent calls in this workspace.</p>
      ) : (
        <div className="divide-y divide-border/40">
          {rows.slice(0, 5).map((row) => (
            <Link
              key={row.id}
              to={`/audit?id=${encodeURIComponent(row.id)}`}
              className="grid grid-cols-[1fr_auto] gap-3 px-3 py-2 text-sm transition-colors hover:bg-muted/30"
            >
              <span className="min-w-0">
                <span className="block truncate font-mono text-xs text-foreground">{row.tool_name}</span>
                <span className="block truncate text-[11px] text-muted-foreground">
                  {row.downstream_server_name || row.client_type || 'gateway'}
                </span>
              </span>
              <Badge variant="outline" tone={auditTone(row.status)}>
                {row.status}
              </Badge>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

export function memoryDetail(stats: MemoryStats | null): string {
  if (!stats) return 'loading'
  const fresh = stats.recency_buckets?.fresh ?? 0
  if (fresh > 0) return `${fresh} fresh`
  return `${stats.pages_equivalent} pages`
}

function auditTone(status: AuditRecord['status']): CommandTone {
  if (status === 'error' || status === 'blocked') return 'critical'
  if (status === 'success' || status === 'ok') return 'success'
  return 'muted'
}
