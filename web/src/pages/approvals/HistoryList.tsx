import { useMemo, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'
import type { ToolApproval } from '@/api/types'
import { formatAbsolute, formatRelative } from '@/pages/tasks/task-utils'
import { ApproverChip, StatusBadge, primaryLabel, surfaceMeta } from './approval-helpers'

type Filter = 'all' | 'approved' | 'denied' | 'timeout' | 'cancelled'
const FILTERS: Filter[] = ['all', 'approved', 'denied', 'timeout', 'cancelled']

// sortNewestFirst guarantees reverse-chronological order client-side even
// if the API contract ever loosens: resolved rows by resolved_at, falling
// back to created_at.
function sortNewestFirst(items: ToolApproval[]): ToolApproval[] {
  return [...items].sort((a, b) => {
    const ta = new Date(a.resolved_at || a.created_at).getTime()
    const tb = new Date(b.resolved_at || b.created_at).getTime()
    return tb - ta
  })
}

export function HistoryList({
  items,
  onOpenDetail,
}: {
  items: ToolApproval[]
  onOpenDetail: (a: ToolApproval) => void
}) {
  const [filter, setFilter] = useState<Filter>('all')

  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const a of items) c[a.status] = (c[a.status] ?? 0) + 1
    return c
  }, [items])

  const rows = useMemo(() => {
    const sorted = sortNewestFirst(items)
    return filter === 'all' ? sorted : sorted.filter((a) => a.status === filter)
  }, [items, filter])

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
          History
        </h2>
        <div className="flex items-center gap-1 text-xs">
          {FILTERS.map((f) => {
            const n = f === 'all' ? items.length : counts[f] ?? 0
            const active = filter === f
            return (
              <button
                key={f}
                type="button"
                onClick={() => setFilter(f)}
                className={cn(
                  'px-2 py-1 font-mono lowercase tracking-wide transition-colors',
                  active
                    ? 'bg-accent text-accent-foreground'
                    : 'text-muted-foreground hover:text-foreground',
                )}
              >
                {f}
                <span className="ml-1 tabular-nums text-muted-foreground/70">{n}</span>
              </button>
            )
          })}
        </div>
      </div>

      <div className="border border-border">
        <Table>
          <TableHeader>
            <TableRow className="border-border hover:bg-transparent">
              <TableHead className="w-[7rem]">When</TableHead>
              <TableHead>Request</TableHead>
              <TableHead className="hidden w-[8rem] sm:table-cell">Surface</TableHead>
              <TableHead className="w-[7rem]">Outcome</TableHead>
              <TableHead className="hidden w-[10rem] md:table-cell">By</TableHead>
              <TableHead className="w-8" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((a) => {
              const surface = surfaceMeta(a.surface)
              const ts = a.resolved_at || a.created_at
              return (
                <TableRow
                  key={a.id}
                  onClick={() => onOpenDetail(a)}
                  className="group cursor-pointer border-border/40 hover:bg-muted/40"
                >
                  <TableCell
                    className="whitespace-nowrap font-mono text-xs text-muted-foreground"
                    title={formatAbsolute(ts)}
                  >
                    {formatRelative(ts)}
                  </TableCell>
                  <TableCell>
                    <div
                      className="max-w-[22rem] truncate font-mono text-sm text-accent-foreground"
                      title={primaryLabel(a)}
                    >
                      {primaryLabel(a)}
                    </div>
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">
                    <span className="inline-flex items-center gap-1 font-mono text-xs uppercase tracking-wider text-muted-foreground">
                      <surface.Icon className="h-2.5 w-2.5" />
                      {surface.label}
                    </span>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={a.status} />
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    <ApproverChip approverType={a.approver_type} approverSessionID={a.approver_session_id} />
                  </TableCell>
                  <TableCell className="text-muted-foreground/40 group-hover:text-muted-foreground">
                    <ChevronRight className="h-4 w-4" />
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>
    </section>
  )
}
