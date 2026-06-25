import { ArrowDown, ArrowUp, Radio } from 'lucide-react'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { AuditFilter, AuditRecord, AuditSort } from '@/api/types'
import { AuditRow, type AuditColumns } from '@/components/audit/AuditRow'
import { cn } from '@/lib/utils'

// Which AuditSort pair a header toggles between. Only time + latency are
// server-sortable; the other headers render as plain (non-interactive) labels.
type SortField = 'time' | 'latency'

function sortFor(field: SortField, current: AuditSort | undefined): AuditSort {
  // Toggle desc <-> asc for the active field; default to desc when switching.
  if (field === 'time') return current === 'time_desc' ? 'time_asc' : 'time_desc'
  return current === 'latency_desc' ? 'latency_asc' : 'latency_desc'
}

function arrowFor(field: SortField, sort: AuditSort | undefined) {
  if (field === 'time' && (sort === 'time_desc' || sort === 'time_asc')) {
    return sort === 'time_desc' ? ArrowDown : ArrowUp
  }
  if (field === 'latency' && (sort === 'latency_desc' || sort === 'latency_asc')) {
    return sort === 'latency_desc' ? ArrowDown : ArrowUp
  }
  return null
}

function SortHead({
  field,
  label,
  align,
  sort,
  onSort,
}: {
  field: SortField
  label: string
  align?: 'right'
  sort?: AuditSort
  onSort?: (sort: AuditSort) => void
}) {
  const Arrow = arrowFor(field, sort)
  if (!onSort) {
    return <span className={cn('inline-flex items-center gap-1', align === 'right' && 'justify-end')}>{label}</span>
  }
  return (
    <button
      type="button"
      onClick={() => onSort(sortFor(field, sort))}
      className={cn(
        'inline-flex items-center gap-1 transition-colors hover:text-foreground',
        Arrow ? 'text-foreground' : 'text-muted-foreground',
      )}
    >
      {label}
      {Arrow && <Arrow className="h-3 w-3 text-primary" />}
    </button>
  )
}

/**
 * AuditTable — the Mission Control list shell. Owns the colgroup widths +
 * sortable header (time / latency) + the empty state, and maps each record to
 * an AuditRow. Selection, density, column visibility, and the live region are
 * all caller-controlled so scoped feeds can reuse the exact same body with a
 * trimmed column set.
 *
 * - `liveCount` marks the first N rows as freshly-streamed (entrance anim).
 * - `onSort` makes the time/latency headers interactive; omit for a static
 *   header (scoped feeds).
 * - `wsName` / `asName` resolve ids to labels.
 */
export function AuditTable({
  records,
  selectedId,
  sort,
  columns,
  dense,
  liveCount = 0,
  loading,
  emptyTitle,
  emptyHint,
  wsName,
  asName,
  onSelect,
  onSort,
  onFilter,
}: {
  records: AuditRecord[]
  selectedId?: string | null
  sort?: AuditSort
  columns?: AuditColumns
  dense?: boolean
  liveCount?: number
  loading?: boolean
  emptyTitle?: string
  emptyHint?: string
  wsName?: (id: string) => string
  asName?: (id: string) => string
  onSelect: (record: AuditRecord) => void
  onSort?: (sort: AuditSort) => void
  onFilter?: (patch: Partial<AuditFilter>) => void
}) {
  const col: Required<AuditColumns> = {
    timestamp: true,
    tool: true,
    workspace: true,
    session: true,
    client: true,
    status: true,
    reason: true,
    cache: true,
    group: true,
    latency: true,
    ...columns,
  }
  const visibleCount = Object.values(col).filter(Boolean).length

  return (
    <Table className="table-fixed">
      <colgroup>
        {col.timestamp && <col className="w-[7rem]" />}
        {col.tool && <col className="w-[18rem]" />}
        {col.workspace && <col className="hidden md:table-column w-[10rem]" />}
        {col.session && <col className="hidden lg:table-column w-[8rem]" />}
        {col.client && <col className="hidden lg:table-column w-[9rem]" />}
        {col.status && <col className="w-[6rem]" />}
        {col.reason && <col />}
        {col.cache && <col className="hidden lg:table-column w-[5rem]" />}
        {col.group && <col className="hidden lg:table-column w-[8rem]" />}
        {col.latency && <col className="hidden sm:table-column w-[5rem]" />}
      </colgroup>
      <TableHeader>
        <TableRow className="border-border/50 hover:bg-transparent">
          {col.timestamp && (
            <TableHead>
              <SortHead field="time" label="Timestamp" sort={sort} onSort={onSort} />
            </TableHead>
          )}
          {col.tool && <TableHead>Tool</TableHead>}
          {col.workspace && <TableHead className="hidden md:table-cell">Workspace</TableHead>}
          {col.session && <TableHead className="hidden lg:table-cell">Session</TableHead>}
          {col.client && <TableHead className="hidden lg:table-cell">Client</TableHead>}
          {col.status && <TableHead>Status</TableHead>}
          {col.reason && <TableHead>Reason</TableHead>}
          {col.cache && <TableHead className="hidden lg:table-cell">Cache</TableHead>}
          {col.group && <TableHead className="hidden lg:table-cell">Group</TableHead>}
          {col.latency && (
            <TableHead className="hidden sm:table-cell text-right">
              <SortHead field="latency" label="Latency" align="right" sort={sort} onSort={onSort} />
            </TableHead>
          )}
        </TableRow>
      </TableHeader>
      <TableBody>
        {records.length === 0 && !loading ? (
          <TableRow>
            <TableCell colSpan={visibleCount} className="h-32">
              <div className="flex flex-col items-center justify-center text-muted-foreground">
                <Radio className="mb-2 h-8 w-8 text-muted-foreground/50" />
                <p className="text-sm">{emptyTitle ?? 'Waiting for events...'}</p>
                <p className="text-xs text-muted-foreground/60">
                  {emptyHint ?? 'New audit records will appear here in real-time'}
                </p>
              </div>
            </TableCell>
          </TableRow>
        ) : (
          records.map((record, idx) => (
            <AuditRow
              key={record.id}
              record={record}
              columns={columns}
              dense={dense}
              selected={selectedId === record.id}
              isLive={idx < liveCount && idx === 0}
              wsName={wsName}
              asName={asName}
              onSelect={onSelect}
              onFilter={onFilter}
            />
          ))
        )}
      </TableBody>
    </Table>
  )
}
