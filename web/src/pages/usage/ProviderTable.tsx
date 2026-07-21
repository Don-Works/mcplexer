import { GripVertical } from 'lucide-react'
import {
  closestCenter,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  TouchSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { ProviderUsage, UsageWindow } from '@/api/usage'
import { ProviderMobileList } from '@/pages/usage/ProviderMobileList'
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

export { lineageStatusLabel } from '@/pages/usage/usageFormat'

export function ProviderTable({
  providers,
  windowDays,
  onReorder,
}: {
  providers: ProviderUsage[]
  windowDays?: number
  onReorder?: (from: string, to: string) => void
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 150, tolerance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
  if (providers.length === 0) return null

  function handleDragEnd(event: DragEndEvent) {
    if (event.over && event.active.id !== event.over.id) {
      onReorder?.(String(event.active.id), String(event.over.id))
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-sm font-medium">
          Provider usage
          {windowDays != null && (
            <span className="ml-2 font-normal text-muted-foreground">
              ({windowDays}-day local window)
            </span>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="p-0">
        <ProviderMobileList providers={providers} onReorder={onReorder} />
        <div className="hidden md:block">
          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={handleDragEnd}
          >
            <SortableContext
              items={providers.map((provider) => provider.provider)}
              strategy={verticalListSortingStrategy}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-10"><span className="sr-only">Order</span></TableHead>
                    <TableHead>Provider</TableHead>
                    <TableHead>Lineage</TableHead>
                    <TableHead className="text-right">Requests</TableHead>
                    <TableHead className="text-right">Tokens</TableHead>
                    <TableHead className="text-right">Cost</TableHead>
                    <TableHead className="min-w-[200px]">Allowance / estimate</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {providers.map((provider) => (
                    <ProviderRow key={provider.provider} provider={provider} />
                  ))}
                </TableBody>
              </Table>
            </SortableContext>
          </DndContext>
        </div>
      </CardContent>
    </Card>
  )
}

function ProviderRow({ provider }: { provider: ProviderUsage }) {
  const totalTokens = observedTokens(provider.observed)
  const allowanceWindows = provider.windows.filter(isAllowanceWindow)
  const hasObserved = hasObservedTotals(provider.observed)
  const {
    attributes,
    listeners,
    setActivatorNodeRef,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: provider.provider })

  return (
    <TableRow
      ref={setNodeRef}
      className={isDragging ? 'relative z-10 bg-accent/40 shadow-lg' : undefined}
      style={{ transform: CSS.Transform.toString(transform), transition }}
    >
      <TableCell className="w-10 px-2">
        <button
          ref={setActivatorNodeRef}
          type="button"
          className="inline-flex h-8 w-8 touch-none items-center justify-center text-muted-foreground hover:bg-muted hover:text-foreground active:cursor-grabbing"
          aria-label={`Reorder ${provider.label}`}
          title="Drag to reorder. Space and arrow keys also work."
          {...attributes}
          {...listeners}
        >
          <GripVertical className="h-4 w-4" aria-hidden="true" />
        </button>
      </TableCell>
      <TableCell>
        <div className="font-medium">{provider.label}</div>
        {provider.plan && (
          <div className="text-xs text-muted-foreground">{provider.plan}</div>
        )}
        {provider.detail && (
          <div className="mt-1 text-xs text-muted-foreground">{provider.detail}</div>
        )}
      </TableCell>
      <TableCell>
        <LineageCell provider={provider} />
      </TableCell>
      <TableCell className="text-right font-mono text-sm">
        {provider.observed.requests > 0 ? formatNumber(provider.observed.requests) : '—'}
      </TableCell>
      <TableCell className="text-right font-mono text-sm">
        {totalTokens > 0 ? formatTokens(totalTokens) : '—'}
      </TableCell>
      <TableCell className="text-right font-mono text-sm">
        <CostCell
          cost={provider.observed.cost_usd}
          kind={provider.observed_cost_kind}
          hasObserved={hasObserved}
        />
      </TableCell>
      <TableCell>
        {allowanceWindows.length > 0 ? (
          <div className="space-y-3">
            {allowanceWindows.map((window) => (
              <WindowBar key={window.id} window={window} />
            ))}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">No allowance or estimate data</span>
        )}
      </TableCell>
    </TableRow>
  )
}

function LineageCell({ provider }: { provider: ProviderUsage }) {
  const allowanceStatus = provider.allowance_status ?? inferAllowanceStatus(provider)
  const observedStatus = inferObservedStatus(provider)
  const isAuthProbe = provider.allowance_source === 'auth'

  return (
    <div className="space-y-2">
      <LineageRow
        label={isAuthProbe ? 'Provider connection' : 'Allowance lineage'}
        status={allowanceStatus}
        statusLabel={isAuthProbe && allowanceStatus === 'ok' ? 'Authenticated' : undefined}
        source={provider.allowance_source_label ?? provider.source_label}
        updatedAt={provider.allowance_updated_at ?? provider.updated_at}
        stale={provider.allowance_stale ?? provider.stale}
        error={provider.allowance_error ?? provider.error}
      />
      <LineageRow
        label="Local observation"
        status={observedStatus}
        source={provider.observed_source_label ?? provider.source_label}
        updatedAt={provider.observed_updated_at}
        missingRuns={provider.observed.accounting_missing_runs}
      />
    </div>
  )
}

function LineageRow({
  label,
  status,
  source,
  updatedAt,
  stale,
  error,
  missingRuns,
  statusLabel,
}: {
  label: string
  status: string
  source?: string
  updatedAt?: string
  stale?: boolean
  error?: string
  missingRuns?: number
  statusLabel?: string
}) {
  return (
    <div className="space-y-1">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <Badge variant={statusVariant(status)} className="text-xs">
        {statusLabel ?? lineageStatusLabel(status)}
      </Badge>
      {source && <div className="text-xs text-muted-foreground">{source}</div>}
      {updatedAt && (
        <div className="text-xs text-muted-foreground">Updated {formatFreshness(updatedAt)}</div>
      )}
      {stale && <div className="text-xs text-amber-500">Stale</div>}
      {missingRuns != null && missingRuns > 0 && (
        <div className="text-xs text-amber-500">{formatNumber(missingRuns)} runs unmeasured</div>
      )}
      {error && (
        <div className="text-xs text-destructive truncate max-w-[180px]" title={error}>
          {error}
        </div>
      )}
    </div>
  )
}

function CostCell({
  cost,
  kind,
  hasObserved,
}: {
  cost: number
  kind?: string
  hasObserved: boolean
}) {
  if (!hasObserved || cost === 0) return <span>—</span>
  const prefix = kind === 'estimate' ? 'Est. ' : ''
  return <span>{prefix}${cost.toFixed(2)}</span>
}

function WindowBar({ window: w }: { window: UsageWindow }) {
  const pct = w.used_percent ?? (w.used != null && w.limit != null && w.limit > 0 ? (w.used / w.limit) * 100 : undefined)

  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-xs">
        <span className="text-muted-foreground">{w.label}</span>
        <span className="font-mono">
          {pct != null ? `${Math.round(pct)}%` : '—'}
        </span>
      </div>
      {pct != null && (
        <div className="h-1.5 w-full overflow-hidden bg-muted">
          <div
            className={progressColor(pct)}
            style={{ width: `${Math.min(100, pct)}%` }}
          />
        </div>
      )}
      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{formatWindowUsed(w)}</span>
        {w.resets_at && (
          <span>Resets {formatResetsAt(w.resets_at)}</span>
        )}
      </div>
    </div>
  )
}

function isAllowanceWindow(window: UsageWindow): boolean {
  return window.used != null || window.used_percent != null || window.limit != null ||
    window.remaining != null || window.resets_at != null
}

function hasObservedTotals(observed: ProviderUsage['observed']): boolean {
  return observed.requests > 0 || (observed.total_tokens ?? 0) > 0 || observed.input_tokens > 0 ||
    observed.output_tokens > 0 || observed.cache_read_tokens > 0 ||
    observed.cache_write_tokens > 0 || observed.cost_usd !== 0 ||
    observed.accounting_missing_runs > 0
}

function inferAllowanceStatus(provider: ProviderUsage): string {
  if (provider.windows.length > 0) {
    return provider.allowance_status ?? provider.status
  }
  return provider.allowance_status ?? 'unavailable'
}

function inferObservedStatus(provider: ProviderUsage): string {
  if (!hasObservedTotals(provider.observed)) return 'unavailable'
  if (provider.observed.accounting_missing_runs > 0) return 'partial'
  return 'ok'
}
