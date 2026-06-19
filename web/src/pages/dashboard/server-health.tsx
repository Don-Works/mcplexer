import { useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import type { ServerHealthEntry } from '@/api/types'

function errorRateColor(rate: number): string {
  if (rate < 5) return 'text-chart-2'
  if (rate < 20) return 'text-amber-400'
  return 'text-destructive'
}

function healthBorderColor(entry: ServerHealthEntry, state: string): string {
  if (state === 'stopped') return 'border-destructive/50'
  if (state === 'external') return 'border-border/50'
  if (entry.error_rate > 25) return 'border-destructive/50'
  if (entry.error_rate > 10) return 'border-amber-500/50'
  return 'border-chart-2/50'
}

export function ServerHealthCards({
  entries,
  downstreams,
}: {
  entries: ServerHealthEntry[]
  downstreams: { server_id: string; state: string; disabled?: boolean }[]
}) {
  const [showIdle, setShowIdle] = useState(false)
  const [showDisabled, setShowDisabled] = useState(false)
  const dsMap = new Map(downstreams.map((d) => [d.server_id, d]))
  const stateMap = new Map(downstreams.map((d) => [d.server_id, d.state]))
  const healthMap = new Map(entries.map((e) => [e.server_id, e]))
  const allServerIds = [...new Set([
    ...downstreams.map((d) => d.server_id),
    ...entries.map((e) => e.server_id),
  ])]

  const disabledIds = allServerIds.filter((id) => dsMap.get(id)?.disabled)
  const enabledIds = allServerIds.filter((id) => !dsMap.get(id)?.disabled)
  const activeIds = enabledIds.filter((id) => {
    const health = healthMap.get(id)
    const state = stateMap.get(id) ?? 'unknown'
    return (health && health.call_count > 0) || (state !== 'stopped' && state !== 'external')
  })
  const idleIds = enabledIds.filter((id) => !activeIds.includes(id))

  const renderCard = (id: string) => {
    const health = healthMap.get(id)
    const state = stateMap.get(id) ?? 'unknown'
    const ds = downstreams.find((d) => d.server_id === id)
    const serverName = health?.server_name ?? ds?.server_id ?? id
    const isDisabled = state === 'disabled'
    const borderColor = isDisabled ? 'border-border/30' : health ? healthBorderColor(health, state) : 'border-border/50'
    return (
      <div
        key={id}
        className={`rounded-lg border-2 ${borderColor} bg-card p-4 space-y-2 ${isDisabled ? 'opacity-50' : ''}`}
      >
        <div className="flex items-center justify-between">
          <span className="text-sm font-medium truncate min-w-0" title={serverName}>{serverName}</span>
          <Badge variant={
            state === 'stopped' ? 'destructive'
            : state === 'disabled' ? 'outline'
            : state === 'external' ? 'outline'
            : 'secondary'
          }>
            {state}
          </Badge>
        </div>
        {!isDisabled && health && health.call_count > 0 ? (
          <div className="grid grid-cols-3 gap-2 text-xs">
            <div>
              <div className="text-muted-foreground">Calls</div>
              <div className="font-mono font-medium">{health.call_count}</div>
            </div>
            <div>
              <div className="text-muted-foreground">Error %</div>
              <div className={`font-mono font-medium ${errorRateColor(health.error_rate)}`}>
                {health.error_rate.toFixed(1)}%
              </div>
            </div>
            <div>
              <div className="text-muted-foreground">Avg Lat.</div>
              <div className="font-mono font-medium">{Math.round(health.avg_latency_ms)}ms</div>
            </div>
          </div>
        ) : !isDisabled ? (
          <div className="text-xs text-muted-foreground">No calls in period</div>
        ) : null}
      </div>
    )
  }

  const collapsibleSection = (
    ids: string[],
    label: string,
    show: boolean,
    setShow: (v: boolean) => void,
  ) => (
    <div>
      <button
        onClick={() => setShow(!show)}
        className="text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        {show ? 'Hide' : 'Show'} {ids.length} {label} server{ids.length !== 1 ? 's' : ''}
        <span className="ml-1">{show ? '\u25B2' : '\u25BC'}</span>
      </button>
      {show && (
        <div className="mt-3 grid gap-3 sm:grid-cols-2">
          {ids.map(renderCard)}
        </div>
      )}
    </div>
  )

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
          Server Health
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {activeIds.length === 0 && idleIds.length === 0 && disabledIds.length === 0 && (
          <div className="py-6 text-center text-xs text-muted-foreground/60">
            No servers configured
          </div>
        )}
        {activeIds.length > 0 && (
          <div className="grid gap-3 sm:grid-cols-2">
            {activeIds.map(renderCard)}
          </div>
        )}
        {idleIds.length > 0 && collapsibleSection(idleIds, 'idle', showIdle, setShowIdle)}
        {disabledIds.length > 0 && collapsibleSection(disabledIds, 'disabled', showDisabled, setShowDisabled)}
      </CardContent>
    </Card>
  )
}
