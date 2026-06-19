import { Link } from 'react-router-dom'
import { Activity, AlertTriangle, GitBranch } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { WorkerSummary } from '@/api/workers'
import { relativeTime, runningCount, statusBadgeClass, summariseModel } from './worker-utils'

interface SummaryProps {
  rows: WorkerSummary[]
  configuredRows: WorkerSummary[]
  ephemeralRows: WorkerSummary[]
  connected: boolean
  lastEventAt: number | null
  lastRefreshAt: number | null
}

export function WorkersRealtimeSummary({
  rows,
  configuredRows,
  ephemeralRows,
  connected,
  lastEventAt,
  lastRefreshAt,
}: SummaryProps) {
  const running = runningCount(rows)
  const activeDelegations = ephemeralRows.filter((row) => row.last_run_status === 'running').length
  const attention = attentionCount(rows)
  const cells = [
    { label: 'configured', value: configuredRows.length, detail: 'scheduled or triggerable workers' },
    {
      label: 'ephemeral',
      value: ephemeralRows.length,
      detail: `${activeDelegations} live delegation context${activeDelegations === 1 ? '' : 's'}`,
    },
    { label: 'running', value: running, detail: 'runner status stream' },
    { label: 'attention', value: attention, detail: 'failed or waiting on approval' },
  ]

  return (
    <div className="grid gap-3 md:grid-cols-4" data-testid="workers-realtime-summary">
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

export function EphemeralWorkersPanel({ rows, total }: { rows: WorkerSummary[]; total: number }) {
  const visible = rows.slice(0, 8)
  const hidden = rows.length - visible.length
  return (
    <section className="space-y-2" data-testid="workers-ephemeral-panel">
      <div className="flex flex-wrap items-center justify-between gap-2 px-1">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold text-foreground">
            <GitBranch className="h-4 w-4 text-sky-300" /> Ephemeral delegation workers
          </h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            One-shot worker contexts created from the Delegations page.
          </p>
        </div>
        <Button variant="outline" size="sm" asChild>
          <Link to="/delegations">Open delegation ledger</Link>
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Delegation</TableHead>
                <TableHead>Model</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Updated</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visible.map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="align-top">
                    <Link to="/delegations" className="font-medium hover:underline">
                      {row.delegation_objective || row.name}
                    </Link>
                    <div className="mt-0.5 flex flex-wrap gap-1.5 text-[10px] text-muted-foreground/70">
                      <span className="truncate font-mono" title={row.delegation_id || row.id}>{row.delegation_id || row.id}</span>
                      {row.delegation_task_id && <span>task {row.delegation_task_id}</span>}
                      {row.delegation_worker_mode && <span>{row.delegation_worker_mode}</span>}
                    </div>
                  </TableCell>
                  <TableCell className="align-top font-mono text-xs text-muted-foreground">
                    {summariseModel(row.model_provider, row.model_id)}
                  </TableCell>
                  <TableCell className="align-top">
                    <Badge variant="outline" className={statusBadgeClass(row.last_run_status)}>
                      {row.last_run_status || 'dispatched'}
                    </Badge>
                  </TableCell>
                  <TableCell className="align-top text-xs text-muted-foreground">
                    {relativeTime(row.last_run_at || row.created_at)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
      {hidden > 0 && (
        <div className="px-1 text-xs text-muted-foreground">
          Showing {visible.length} of {total} delegation workers. Open the ledger for full context trees.
        </div>
      )}
    </section>
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
