import { useNavigate } from 'react-router-dom'
import { Activity } from 'lucide-react'

import type { AuditFilter, AuditRecord } from '@/api/types'
import type { MemoryStats } from '@/api/memory'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { AuditTable } from '@/components/audit/AuditTable'
import type { AuditColumns } from '@/components/audit/AuditRow'
import { AuditAlertsRail } from '@/components/audit/AuditAlertsRail'
import { useAuditAlerts } from '@/hooks/use-audit-alerts'
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

// Compact column set for the workspace-scoped tile: the workspace is implicit,
// so drop it along with session / client / cache / group / latency. The row
// click deep-links to /audit?id= (the exact-match drawer fallback), matching
// the bespoke <Link> the tile used before.
const SCOPED_COLUMNS: AuditColumns = {
  timestamp: true,
  tool: true,
  status: true,
  reason: true,
  workspace: false,
  session: false,
  client: false,
  cache: false,
  group: false,
  latency: false,
}

// Serialize an alert's ready-made AuditFilter into /audit query params so the
// page opens pre-scoped. Only the params AuditPage reads back are emitted.
function auditHrefFor(filter: AuditFilter): string {
  const params = new URLSearchParams()
  const set = (k: string, v: string | number | undefined) => {
    if (v !== undefined && v !== '') params.set(k, String(v))
  }
  set('workspace_id', filter.workspace_id)
  set('tool_name', filter.tool_name)
  set('status', filter.status)
  set('execution_id', filter.execution_id)
  set('session_id', filter.session_id)
  set('actor_kind', filter.actor_kind)
  set('client_type', filter.client_type)
  set('downstream_server_id', filter.downstream_server_id)
  set('route_rule_id', filter.route_rule_id)
  set('min_latency_ms', filter.min_latency_ms)
  set('q', filter.q)
  const qs = params.toString()
  return qs ? `/audit?${qs}` : '/audit'
}

export function RecentAudit({ rows, workspaceId }: { rows: AuditRecord[]; workspaceId: string }) {
  const navigate = useNavigate()
  const { alerts, loading: alertsLoading } = useAuditAlerts({ workspace_id: workspaceId })
  const showAlerts = alerts.length > 0 || alertsLoading

  return (
    <div className="border border-border/50">
      <div className="flex items-center justify-between gap-3 border-b border-border/50 px-3 py-2">
        <h3 className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
          Recent tool calls
        </h3>
        <Activity className="h-3.5 w-3.5 text-muted-foreground" />
      </div>
      {showAlerts && (
        <div className="border-b border-border/50 p-3">
          <AuditAlertsRail
            alerts={alerts}
            loading={alertsLoading}
            onApplyFilter={(filter) => navigate(auditHrefFor(filter))}
          />
        </div>
      )}
      {rows.length === 0 ? (
        <p className="px-3 py-4 text-sm text-muted-foreground">No recent calls in this workspace.</p>
      ) : (
        <AuditTable
          records={rows.slice(0, 5)}
          columns={SCOPED_COLUMNS}
          dense
          onSelect={(record) => navigate(`/audit?id=${encodeURIComponent(record.id)}`)}
        />
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
