import { Badge } from '@/components/ui/badge'
import type { ProviderUsage, UsageWindow } from '@/api/usage'
import {
  formatFreshness,
  formatNumber,
  formatResetsAt,
  formatTokens,
  formatWindowUsed,
  lineageStatusLabel,
  observedTokens,
  progressColor,
  statusVariant,
} from '@/pages/usage/usageFormat'

export function ProviderMobileList({ providers }: { providers: ProviderUsage[] }) {
  return (
    <div className="divide-y md:hidden">
      {providers.map((provider) => (
        <MobileProvider key={provider.provider} provider={provider} />
      ))}
    </div>
  )
}

function MobileProvider({ provider }: { provider: ProviderUsage }) {
  const observed = hasObserved(provider)
  const windows = provider.windows.filter(hasAllowanceData)
  const isAuth = provider.allowance_source === 'auth'
  const allowanceStatus = provider.allowance_status ?? (windows.length > 0 ? provider.status : 'unavailable')

  return (
    <article className="space-y-4 p-4">
      <div>
        <div className="font-medium">{provider.label}</div>
        {provider.plan && <div className="text-xs text-muted-foreground">{provider.plan}</div>}
        {provider.detail && <div className="mt-1 text-xs text-muted-foreground">{provider.detail}</div>}
      </div>

      <div className="grid grid-cols-2 gap-3">
        <MobileSource
          label={isAuth ? 'Provider connection' : 'Live allowance'}
          status={allowanceStatus}
          statusLabel={isAuth && allowanceStatus === 'ok' ? 'Authenticated' : undefined}
          source={provider.allowance_source_label ?? provider.source_label}
          updatedAt={provider.allowance_updated_at ?? provider.updated_at}
        />
        <MobileSource
          label="Local observation"
          status={observed ? (provider.observed.accounting_missing_runs > 0 ? 'partial' : 'ok') : 'unavailable'}
          source={provider.observed_source_label ?? provider.source_label}
          updatedAt={provider.observed_updated_at}
        />
      </div>

      <div className="grid grid-cols-3 gap-3 border-y py-3">
        <MobileMetric
          label="Requests"
          value={provider.observed.requests > 0 ? formatNumber(provider.observed.requests) : '—'}
        />
        <MobileMetric
          label="Tokens"
          value={observedTokens(provider.observed) > 0
            ? formatTokens(observedTokens(provider.observed))
            : '—'}
        />
        <MobileMetric
          label="Cost"
          value={observed && provider.observed.cost_usd !== 0
            ? `${provider.observed_cost_kind === 'estimate' ? 'Est. ' : ''}$${provider.observed.cost_usd.toFixed(2)}`
            : '—'}
        />
      </div>

      <div className="space-y-3">
        <div className="text-[10px] uppercase tracking-wide text-muted-foreground">Allowance windows</div>
        {windows.length > 0
          ? windows.map((window) => <MobileWindow key={window.id} window={window} />)
          : <div className="text-xs text-muted-foreground">No live allowance data</div>}
      </div>
    </article>
  )
}

function MobileSource({
  label,
  status,
  statusLabel,
  source,
  updatedAt,
}: {
  label: string
  status: string
  statusLabel?: string
  source?: string
  updatedAt?: string
}) {
  return (
    <div className="min-w-0 space-y-1">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <Badge variant={statusVariant(status)}>{statusLabel ?? lineageStatusLabel(status)}</Badge>
      {source && <div className="break-words text-xs text-muted-foreground">{source}</div>}
      {updatedAt && <div className="text-xs text-muted-foreground">Updated {formatFreshness(updatedAt)}</div>}
    </div>
  )
}

function MobileMetric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 break-words font-mono text-sm">{value}</div>
    </div>
  )
}

function MobileWindow({ window }: { window: UsageWindow }) {
  const percent = window.used_percent ?? (
    window.used != null && window.limit != null && window.limit > 0
      ? (window.used / window.limit) * 100
      : undefined
  )
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between gap-3 text-xs">
        <span>{window.label}</span>
        <span className="font-mono">{percent == null ? '—' : `${Math.round(percent)}%`}</span>
      </div>
      {percent != null && (
        <div className="h-1.5 overflow-hidden bg-muted">
          <div className={progressColor(percent)} style={{ width: `${Math.min(100, percent)}%` }} />
        </div>
      )}
      <div className="flex justify-between gap-3 text-xs text-muted-foreground">
        <span>{formatWindowUsed(window)}</span>
        {window.resets_at && <span>Resets {formatResetsAt(window.resets_at)}</span>}
      </div>
    </div>
  )
}

function hasObserved(provider: ProviderUsage): boolean {
  const observed = provider.observed
  return observed.requests > 0 || (observed.total_tokens ?? 0) > 0 ||
    observed.input_tokens > 0 || observed.output_tokens > 0 ||
    observed.cache_read_tokens > 0 || observed.cache_write_tokens > 0 ||
    observed.cost_usd !== 0 || observed.accounting_missing_runs > 0
}

function hasAllowanceData(window: UsageWindow): boolean {
  return window.used_percent != null || window.limit != null ||
    window.remaining != null || window.resets_at != null
}
