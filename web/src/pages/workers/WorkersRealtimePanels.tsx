import { Activity, AlertTriangle } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import type { WorkerSummary } from '@/api/workers'
import { relativeTime, runningCount } from './worker-utils'

interface SummaryProps {
  configuredRows: WorkerSummary[]
  connected: boolean
  lastEventAt: number | null
  lastRefreshAt: number | null
}

export function WorkersRealtimeSummary({
  configuredRows,
  connected,
  lastEventAt,
  lastRefreshAt,
}: SummaryProps) {
  const running = runningCount(configuredRows)
  const attention = attentionCount(configuredRows)
  const cells = [
    { label: 'configured', value: configuredRows.length, detail: 'scheduled or triggerable workers' },
    { label: 'running', value: running, detail: 'runner status stream' },
    { label: 'attention', value: attention, detail: 'failed or waiting on approval' },
  ]

  return (
    <div className="grid gap-3 md:grid-cols-3" data-testid="workers-realtime-summary">
      {cells.map((cell) => (
        <Card key={cell.label}>
          <CardContent className="p-4">
            <div className="flex items-center justify-between gap-2">
              <div className="text-[11px] font-semibold uppercase text-muted-foreground">
                {cell.label}
              </div>
              {cell.label === 'running' && (
                <Badge
                  variant="outline"
                  className={
                    connected
                      ? 'border-sky-500/40 text-sky-300'
                      : 'border-border text-muted-foreground'
                  }
                >
                  <Activity className="mr-1 h-3 w-3" />
                  {connected ? 'live' : 'offline'}
                </Badge>
              )}
              {cell.label === 'attention' && attention > 0 && (
                <AlertTriangle className="h-4 w-4 text-amber-300" />
              )}
            </div>
            <div className="mt-2 text-2xl font-semibold tabular-nums">{cell.value}</div>
            <div className="mt-1 text-xs text-muted-foreground">{cell.detail}</div>
            {cell.label === 'running' && (
              <div className="mt-1 font-mono text-[10px] text-muted-foreground/70">
                {liveStamp(lastEventAt, lastRefreshAt)}
              </div>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

function attentionCount(rows: WorkerSummary[]): number {
  return rows.filter((row) =>
    row.last_run_status === 'failure' ||
    row.last_run_status === 'cap_exceeded' ||
    row.last_run_status === 'rejected' ||
    row.last_run_status === 'awaiting_approval'
  ).length
}

function liveStamp(lastEventAt: number | null, lastRefreshAt: number | null): string {
  const ts = lastEventAt ?? lastRefreshAt
  if (!ts) return 'waiting for first snapshot'
  const label = lastEventAt ? 'event' : 'snapshot'
  return `${label} ${relativeTime(new Date(ts).toISOString())}`
}
