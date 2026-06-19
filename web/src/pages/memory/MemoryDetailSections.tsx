// MemoryDetailSections — drawer sub-components extracted out of
// MemoryDetailDrawer.tsx to keep that file under the 300-line guideline.
// Three components: Section (collapsible header + body), KV (label/value
// row), and ProvenanceSection (lazy-open provenance trail).

import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Loader2,
  Pin,
  PinOff,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { CopyButton } from '@/components/ui/copy-button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import type { MemoryEntry } from '@/api/memory'

export function Section({
  label,
  children,
  defaultMuted,
}: {
  label: string
  children: React.ReactNode
  defaultMuted?: boolean
}) {
  return (
    <section className="border-b border-border/30 py-4 last:border-b-0">
      <h3
        className={cn(
          'mb-2.5 text-[10px] font-semibold uppercase tracking-[0.12em]',
          defaultMuted ? 'text-muted-foreground/60' : 'text-muted-foreground',
        )}
      >
        {label}
      </h3>
      <div className="space-y-2.5">{children}</div>
    </section>
  )
}

export function KV({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex w-28 shrink-0 items-center gap-1 pt-0.5 text-xs text-muted-foreground">
        <span>{label}</span>
      </div>
      <div className="flex min-w-0 flex-1 items-start gap-2">{children}</div>
    </div>
  )
}

export function MemoryActions({
  entry,
  busy,
  onTogglePin,
  onInvalidate,
  onDelete,
}: {
  entry: MemoryEntry
  busy: 'invalidate' | 'delete' | 'pin' | null
  onTogglePin?: () => void
  onInvalidate?: () => void
  onDelete?: () => void
}) {
  const invalidated = !!entry.t_valid_end
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-border/30 pb-4">
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="outline"
            size="sm"
            disabled={!onTogglePin || busy === 'pin'}
            onClick={onTogglePin}
            className="gap-1.5"
            data-testid="memory-detail-pin"
          >
            {busy === 'pin' ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : entry.pinned ? (
              <PinOff className="h-3.5 w-3.5" />
            ) : (
              <Pin className="h-3.5 w-3.5" />
            )}
            {entry.pinned ? 'Unpin' : 'Pin'}
          </Button>
        </TooltipTrigger>
        <TooltipContent>
          {onTogglePin ? 'Mark this memory as pinned' : 'Pin endpoint not wired yet'}
        </TooltipContent>
      </Tooltip>
      <Button
        variant="outline"
        size="sm"
        disabled={!onInvalidate || invalidated || busy !== null}
        onClick={onInvalidate}
        className="gap-1.5 border-amber-500/30 text-amber-300 hover:bg-amber-500/5"
        data-testid="memory-detail-invalidate"
      >
        <ShieldAlert className="h-3.5 w-3.5" />
        Invalidate
      </Button>
      <Button
        variant="outline"
        size="sm"
        disabled={!onDelete || busy !== null}
        onClick={onDelete}
        className="gap-1.5 border-destructive/30 text-destructive hover:bg-destructive/5"
        data-testid="memory-detail-delete"
      >
        <Trash2 className="h-3.5 w-3.5" />
        Delete
      </Button>
    </div>
  )
}

export function DrawerFooterNav({
  onPrev,
  onNext,
  hasPrev,
  hasNext,
}: {
  onPrev?: () => void
  onNext?: () => void
  hasPrev?: boolean
  hasNext?: boolean
}) {
  if (!onPrev && !onNext) return null
  return (
    <div className="flex items-center justify-between border-t border-border/60 bg-background/40 px-5 py-3 text-xs text-muted-foreground">
      <div className="flex items-center gap-1">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              disabled={!hasPrev}
              onClick={() => onPrev?.()}
              data-testid="memory-detail-prev"
            >
              <ArrowUp className="h-3 w-3" />
              Prev
            </Button>
          </TooltipTrigger>
          <TooltipContent>k or ↑</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              disabled={!hasNext}
              onClick={() => onNext?.()}
              data-testid="memory-detail-next"
            >
              <ArrowDown className="h-3 w-3" />
              Next
            </Button>
          </TooltipTrigger>
          <TooltipContent>j or ↓</TooltipContent>
        </Tooltip>
      </div>
      <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground/70">
        esc to close
      </span>
    </div>
  )
}

export function ProvenanceSection({
  entry,
  open,
  onToggle,
}: {
  entry: MemoryEntry
  open: boolean
  onToggle: () => void
}) {
  const hasProvenance =
    entry.source_session_id ||
    entry.source_peer_id ||
    entry.source_tool_call_id ||
    entry.origin_peer_id ||
    entry.worker_id ||
    entry.run_id
  if (!hasProvenance) return null
  return (
    <section className="border-b border-border/30 py-4 last:border-b-0">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-1.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-muted-foreground transition-colors hover:text-foreground"
        data-testid="memory-detail-provenance-toggle"
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        Provenance
      </button>
      {open && (
        <div className="mt-2.5 space-y-2.5">
          {entry.source_session_id && (
            <KV label="Session">
              <code className="font-mono text-xs text-cyan-300 break-all">
                {entry.source_session_id}
              </code>
              <CopyButton value={entry.source_session_id} />
            </KV>
          )}
          {entry.source_tool_call_id && (
            <KV label="Tool call">
              <code className="font-mono text-xs text-violet-300 break-all">
                {entry.source_tool_call_id}
              </code>
              <CopyButton value={entry.source_tool_call_id} />
            </KV>
          )}
          {entry.source_peer_id && (
            <KV label="Source peer">
              <code className="font-mono text-xs text-emerald-300 break-all">
                {entry.source_peer_id}
              </code>
              <CopyButton value={entry.source_peer_id} />
            </KV>
          )}
          {entry.origin_peer_id && (
            <KV label="Origin peer">
              <code className="font-mono text-xs text-emerald-300 break-all">
                {entry.origin_peer_id}
              </code>
              <CopyButton value={entry.origin_peer_id} />
            </KV>
          )}
          {entry.worker_id && (
            <KV label="Worker">
              <code className="font-mono text-xs text-foreground break-all">
                {entry.worker_id}
              </code>
              <CopyButton value={entry.worker_id} />
            </KV>
          )}
          {entry.run_id && (
            <KV label="Run">
              <code className="font-mono text-xs text-foreground break-all">
                {entry.run_id}
              </code>
              <CopyButton value={entry.run_id} />
            </KV>
          )}
        </div>
      )}
    </section>
  )
}
