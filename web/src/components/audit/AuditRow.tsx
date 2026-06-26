import { Badge } from '@/components/ui/badge'
import { TableCell, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Bot, Layers, Monitor } from 'lucide-react'
import type { AuditFilter, AuditRecord } from '@/api/types'
import { ReasonBadge, SecretEventBadge } from '@/components/audit/AuditInspector'
import { classifySecretEvent, normalizeStatus } from '@/lib/audit-semantics'
import { cn } from '@/lib/utils'

// AuditColumns gates which cells render. All default to visible; scoped feeds
// pass a subset (e.g. a connection drawer hides Workspace + Client). The cells
// keep their responsive `hidden …:table-cell` classes so the column also
// respects breakpoints — set false to drop it entirely regardless of width.
export interface AuditColumns {
  timestamp?: boolean
  tool?: boolean
  workspace?: boolean
  session?: boolean
  client?: boolean
  status?: boolean
  reason?: boolean
  cache?: boolean
  group?: boolean
  latency?: boolean
}

const ALL_COLUMNS: Required<AuditColumns> = {
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
}

// The gateway stores `model` as the MCP clientInfo "<name>/<version>" hint
// (e.g. "claude-code/1.0.5"), which repeats the harness name already shown in
// `client_type`. For the compact list view, strip that redundant prefix so the
// secondary line reads as just the version/model tail. Returns '' when the
// model adds nothing beyond the harness name.
function harnessModelTail(record: AuditRecord): string {
  const harness = record.client_type ?? ''
  let model = record.model ?? ''
  if (model && harness && model.startsWith(harness + '/')) {
    model = model.slice(harness.length + 1)
  }
  return model === harness ? '' : model
}

/**
 * AuditRow — THE canonical audit-record row, shared by the Mission Control
 * table and every scoped feed (connection drawer, dashboard attention card).
 * Encapsulates the timestamp / tool / secret-badge / workspace / session /
 * client / status / reason / cache / group / latency cells.
 *
 * - `onSelect` opens the inspector for this record.
 * - `onFilter` (optional) wires the clickable session/execution badges to a
 *   filter patch; omit it to render those ids as plain (non-clickable) badges.
 * - `isLive` plays the entrance animation (only meaningful for a freshly
 *   streamed row at the top of the list).
 * - `columns` hides cells for narrow scoped contexts.
 * - `wsName` / `asName` resolve ids to labels; pass identity fns if unneeded.
 */
export function AuditRow({
  record,
  selected,
  dense,
  isLive,
  columns,
  wsName,
  asName,
  onSelect,
  onFilter,
}: {
  record: AuditRecord
  selected?: boolean
  dense?: boolean
  isLive?: boolean
  columns?: AuditColumns
  wsName?: (id: string) => string
  asName?: (id: string) => string
  onSelect: (record: AuditRecord) => void
  onFilter?: (patch: Partial<AuditFilter>) => void
}) {
  const col = { ...ALL_COLUMNS, ...columns }
  const ws = wsName ?? ((id: string) => id)
  const as = asName ?? ((id: string) => id)
  const tone = normalizeStatus(record.status)
  const cellPad = dense ? 'py-1.5' : ''

  return (
    <TableRow
      tabIndex={0}
      aria-label={`View audit record for ${record.tool_name}`}
      aria-selected={selected}
      data-state={selected ? 'selected' : undefined}
      className={cn(
        'cursor-pointer border-border/30 hover:bg-muted/30 focus-visible:bg-muted/30 focus-visible:outline-none',
        selected && 'bg-muted/40',
        isLive && 'animate-[audit-in_0.3s_ease-out]',
      )}
      onClick={() => onSelect(record)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect(record)
        }
      }}
    >
      {col.timestamp && (
        <TableCell className={cn('w-[7rem] whitespace-nowrap font-mono text-xs text-muted-foreground', cellPad)}>
          <Tooltip>
            <TooltipTrigger asChild>
              <span>{new Date(record.timestamp).toLocaleTimeString()}</span>
            </TooltipTrigger>
            <TooltipContent>{new Date(record.timestamp).toLocaleString()}</TooltipContent>
          </Tooltip>
        </TableCell>
      )}

      {col.tool && (
        <TableCell className={cn('w-[18rem]', cellPad)}>
          <div className="max-w-[20rem] truncate font-mono text-sm text-accent-foreground" title={record.tool_name}>
            {record.tool_name}
          </div>
          {classifySecretEvent(record.tool_name) && (
            <div className="mt-1 flex items-center gap-1.5">
              <SecretEventBadge toolName={record.tool_name} />
              {record.auth_scope_id && (
                <span className="truncate text-[11px] text-muted-foreground" title={as(record.auth_scope_id)}>
                  {as(record.auth_scope_id)}
                </span>
              )}
            </div>
          )}
        </TableCell>
      )}

      {col.workspace && (
        <TableCell className={cn('hidden w-[10rem] text-muted-foreground md:table-cell', cellPad)}>
          <div className="max-w-[10rem] truncate">
            {record.workspace_name || (record.workspace_id ? ws(record.workspace_id) : '-')}
          </div>
        </TableCell>
      )}

      {col.session && (
        <TableCell className={cn('hidden w-[8rem] 2xl:table-cell', cellPad)}>
          {record.session_id && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Badge
                  variant="outline"
                  className={cn(
                    'border-cyan-500/40 text-cyan-400',
                    onFilter && 'cursor-pointer hover:bg-cyan-500/10',
                  )}
                  onClick={
                    onFilter
                      ? (e) => {
                          e.stopPropagation()
                          onFilter({ session_id: record.session_id })
                        }
                      : undefined
                  }
                >
                  <Monitor className="mr-1 h-3 w-3" />
                  {record.session_id.slice(0, 8)}
                </Badge>
              </TooltipTrigger>
              <TooltipContent>
                {onFilter ? 'View all calls in this session' : record.session_id}
              </TooltipContent>
            </Tooltip>
          )}
        </TableCell>
      )}

      {col.client && (
        <TableCell className={cn('hidden w-[9rem] 2xl:table-cell', cellPad)}>
          {record.client_type ? (
            <div className="min-w-0">
              <div className="flex items-center gap-1 text-xs text-foreground" title={record.client_type}>
                <Bot className="h-3 w-3 shrink-0 opacity-60" />
                <span className="truncate">{record.client_type}</span>
              </div>
              {harnessModelTail(record) && (
                <div className="mt-0.5 truncate pl-4 font-mono text-[10px] text-muted-foreground/70" title={record.model}>
                  {harnessModelTail(record)}
                </div>
              )}
            </div>
          ) : (
            <span className="text-muted-foreground/40">-</span>
          )}
        </TableCell>
      )}

      {col.status && (
        <TableCell className={cn('w-[6rem]', cellPad)}>
          <Badge
            variant={tone === 'success' ? 'secondary' : tone === 'blocked' ? 'outline' : 'destructive'}
            className={tone === 'blocked' ? 'border-amber-500/40 text-amber-500' : ''}
          >
            {record.status}
          </Badge>
        </TableCell>
      )}

      {col.reason && (
        <TableCell className={cn('align-top', cellPad)}>
          <ReasonBadge record={record} />
        </TableCell>
      )}

      {col.cache && (
        <TableCell className={cn('hidden w-[5rem] 2xl:table-cell', cellPad)}>
          {record.cache_hit && (
            <Badge variant="outline" className="border-blue-500/40 text-blue-400">
              cached
            </Badge>
          )}
        </TableCell>
      )}

      {col.group && (
        <TableCell className={cn('hidden w-[8rem] 2xl:table-cell', cellPad)}>
          {record.execution_id && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Badge
                  variant="outline"
                  className={cn(
                    'border-violet-500/40 text-violet-400',
                    onFilter && 'cursor-pointer hover:bg-violet-500/10',
                  )}
                  onClick={
                    onFilter
                      ? (e) => {
                          e.stopPropagation()
                          onFilter({ execution_id: record.execution_id })
                        }
                      : undefined
                  }
                >
                  <Layers className="mr-1 h-3 w-3" />
                  {record.execution_id.slice(0, 8)}
                </Badge>
              </TooltipTrigger>
              <TooltipContent>
                {onFilter ? 'View all calls in this execution' : record.execution_id}
              </TooltipContent>
            </Tooltip>
          )}
        </TableCell>
      )}

      {col.latency && (
        <TableCell className={cn('hidden w-[5.5rem] pl-4 text-right font-mono text-sm text-muted-foreground tabular-nums sm:table-cell', cellPad)}>
          {record.latency_ms}ms
        </TableCell>
      )}
    </TableRow>
  )
}
