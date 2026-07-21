// MemoryTable — extracted from MemoryListPage so the page stays under
// 300 lines. Renders the sortable table of memories + the empty-state
// row. The page owns selection + filtering; this component is purely
// presentational.

import { Brain } from 'lucide-react'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { MemoryEntry } from '@/api/memory'
import {
  KindBadge,
  PreviewSnippet,
  ScopeBadge,
  SourceChip,
  TagChips,
} from './memory-primitives'
import { parseTags, relativeTime, scopeOf } from './memory-utils'
import { cn } from '@/lib/utils'

interface Props {
  rows: MemoryEntry[]
  loading: boolean
  selectedId?: string | null
  onSelect: (entry: MemoryEntry) => void
  hasQuery: boolean
}

export function MemoryTable({ rows, loading, selectedId, onSelect, hasQuery }: Props) {
  return (
    <Table className="table-fixed">
      <colgroup>
        <col className="w-[7rem]" />
        <col className="w-[16rem]" />
        <col className="hidden md:table-column w-[5rem]" />
        <col className="hidden md:table-column w-[6rem]" />
        <col className="hidden lg:table-column w-[6rem]" />
        <col className="hidden lg:table-column w-[12rem]" />
        <col />
      </colgroup>
      <TableHeader>
        <TableRow className="border-border/50 hover:bg-transparent">
          <TableHead>Time</TableHead>
          <TableHead>Name</TableHead>
          <TableHead className="hidden md:table-cell">Kind</TableHead>
          <TableHead className="hidden md:table-cell">Scope</TableHead>
          <TableHead className="hidden lg:table-cell">Source</TableHead>
          <TableHead className="hidden lg:table-cell">Tags</TableHead>
          <TableHead>Preview</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.length === 0 && !loading ? (
          <TableRow>
            <TableCell colSpan={7} className="h-40">
              <div className="flex flex-col items-center justify-center text-muted-foreground">
                <Brain className="mb-2 h-7 w-7 text-muted-foreground/40" />
                <p className="text-sm">No memories match this view</p>
                <p className="text-xs text-muted-foreground/60">
                  {hasQuery
                    ? 'Try a different query, or relax your filters.'
                    : 'Once an agent writes a memory, it will appear here.'}
                </p>
              </div>
            </TableCell>
          </TableRow>
        ) : (
          rows.map((entry) => (
            <MemoryRow
              key={entry.id}
              entry={entry}
              selected={selectedId === entry.id}
              onSelect={() => onSelect(entry)}
            />
          ))
        )}
      </TableBody>
    </Table>
  )
}

function MemoryRow({
  entry,
  selected,
  onSelect,
}: {
  entry: MemoryEntry
  selected: boolean
  onSelect: () => void
}) {
  const invalidated = !!entry.t_valid_end
  return (
    <TableRow
      className={cn(
        'cursor-pointer border-border/30 hover:bg-muted/30',
        invalidated && 'opacity-50',
        selected && 'bg-primary/5',
      )}
      onClick={onSelect}
      data-testid={`memory-row-${entry.id}`}
    >
      <TableCell className="whitespace-nowrap font-mono text-[11px] text-muted-foreground">
        <Tooltip>
          <TooltipTrigger asChild>
            <span>{relativeTime(entry.created_at)}</span>
          </TooltipTrigger>
          <TooltipContent>
            {new Date(entry.created_at).toLocaleString()}
          </TooltipContent>
        </Tooltip>
      </TableCell>
      <TableCell>
        <div className="flex flex-col gap-0.5">
          <span
            className="truncate font-mono text-[13px] text-foreground"
            title={entry.name}
          >
            {entry.name}
          </span>
          {entry.pinned && (
            <span className="font-mono text-[9px] uppercase tracking-wider text-amber-300/90">
              pinned
            </span>
          )}
        </div>
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <KindBadge kind={entry.kind} />
      </TableCell>
      <TableCell className="hidden md:table-cell">
        <ScopeBadge scope={scopeOf(entry)} />
      </TableCell>
      <TableCell className="hidden lg:table-cell">
        <SourceChip source={entry.source_kind} />
      </TableCell>
      <TableCell className="hidden lg:table-cell">
        <TagChips tags={parseTags(entry.tags)} max={3} />
      </TableCell>
      <TableCell>
        <PreviewSnippet content={entry.content} />
      </TableCell>
    </TableRow>
  )
}
