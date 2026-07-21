import { AlertTriangle, ShieldAlert, Filter } from 'lucide-react'
import type { AuditAlert, AuditFilter } from '@/api/types'
import { cn } from '@/lib/utils'

// Severity → dot color. NO side-stripe — the dot + count carry the weight.
const SEVERITY_DOT: Record<AuditAlert['severity'], string> = {
  info: 'bg-muted-foreground/50',
  warning: 'bg-amber-500',
  critical: 'bg-destructive',
}

const SEVERITY_RING: Record<AuditAlert['severity'], string> = {
  info: '',
  warning: 'ring-1 ring-amber-500/20',
  critical: 'ring-1 ring-destructive/25',
}

function AlertItem({
  alert,
  onApplyFilter,
}: {
  alert: AuditAlert
  onApplyFilter?: (filter: AuditFilter) => void
}) {
  const Icon = alert.kind === 'security' ? ShieldAlert : AlertTriangle
  return (
    <div
      className={cn(
        'group flex gap-2.5 border border-border/60 bg-card/40 p-2.5 transition-colors hover:bg-muted/30',
        SEVERITY_RING[alert.severity],
      )}
    >
      <span className="mt-1 flex h-2 w-2 shrink-0 items-center">
        <span className={cn('h-2 w-2 rounded-full', SEVERITY_DOT[alert.severity])} />
      </span>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex items-start gap-2">
          <Icon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <p className="min-w-0 flex-1 text-sm font-medium leading-snug text-foreground">
            {alert.title}
          </p>
          <span className="shrink-0 font-mono text-xs tabular-nums text-muted-foreground">
            {alert.count}
          </span>
        </div>
        <p className="text-xs leading-relaxed text-muted-foreground">{alert.detail}</p>
        {(alert.metric || alert.tool_name) && (
          <p className="font-mono text-[10px] text-muted-foreground/70">
            {alert.tool_name && <span className="text-foreground/70">{alert.tool_name}</span>}
            {alert.tool_name && alert.metric && ' · '}
            {alert.metric && (
              <>
                {alert.metric}
                {alert.baseline !== undefined && ` (baseline ${alert.baseline})`}
              </>
            )}
          </p>
        )}
        {onApplyFilter && (
          <button
            type="button"
            onClick={() => onApplyFilter(alert.filter)}
            className="inline-flex items-center gap-1 border border-dashed border-border px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground"
          >
            <Filter className="h-2.5 w-2.5" />
            Inspect
          </button>
        )}
      </div>
    </div>
  )
}

/**
 * AuditAlertsRail — renders the gateway's anomaly + security alerts as a
 * compact list. Severity is a leading dot (info=muted, warning=amber,
 * critical=red), the count sits right-aligned, and each row offers an
 * "Inspect" action that applies the alert's ready-made filter to the page.
 */
export function AuditAlertsRail({
  alerts,
  loading,
  onApplyFilter,
  className,
}: {
  alerts: AuditAlert[]
  loading?: boolean
  onApplyFilter?: (filter: AuditFilter) => void
  className?: string
}) {
  if (loading && alerts.length === 0) {
    return (
      <div className={cn('flex items-center gap-2 p-3 text-xs text-muted-foreground', className)}>
        <span className="h-1.5 w-1.5 rounded-full bg-primary/60" />
        Checking for alerts…
      </div>
    )
  }

  if (alerts.length === 0) {
    return (
      <div className={cn('flex flex-col items-center gap-1.5 p-6 text-center', className)}>
        <ShieldAlert className="h-6 w-6 text-muted-foreground/40" />
        <p className="text-sm text-muted-foreground">No active alerts</p>
        <p className="text-xs text-muted-foreground/60">
          Anomalies and security signals will surface here
        </p>
      </div>
    )
  }

  return (
    <div className={cn('space-y-2', className)}>
      {alerts.map((alert) => (
        <AlertItem key={alert.id} alert={alert} onApplyFilter={onApplyFilter} />
      ))}
    </div>
  )
}
