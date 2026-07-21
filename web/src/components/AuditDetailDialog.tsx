import { useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { CopyButton } from '@/components/ui/copy-button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { AuditRecord } from '@/api/types'
import { ArrowDown, ArrowUp } from 'lucide-react'
import {
  AuditInspector,
  StatusChip,
  Timestamp,
} from '@/components/audit/AuditInspector'
import { getErrorReason } from '@/lib/audit-semantics'

// ReasonBadge + SecretEventBadge moved into AuditInspector (so the row + the
// inspector share one definition). Re-export here to preserve every existing
// import path (`@/components/AuditDetailDialog`).
export { ReasonBadge, SecretEventBadge } from '@/components/audit/AuditInspector'

export function AuditDetailDialog({
  record,
  onClose,
  wsName,
  asName,
  onPrev,
  onNext,
  hasPrev,
  hasNext,
}: {
  record: AuditRecord | null
  onClose: () => void
  wsName: (id: string) => string
  asName: (id: string) => string
  onPrev?: () => void
  onNext?: () => void
  hasPrev?: boolean
  hasNext?: boolean
}) {
  // Keyboard nav while the sheet is open.
  useEffect(() => {
    if (!record) return
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) {
        return
      }
      if ((e.key === 'j' || e.key === 'ArrowDown') && onNext && hasNext) {
        e.preventDefault()
        onNext()
      } else if ((e.key === 'k' || e.key === 'ArrowUp') && onPrev && hasPrev) {
        e.preventDefault()
        onPrev()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [record, onPrev, onNext, hasPrev, hasNext])

  if (!record) return null

  const status = record.status
  const reason = getErrorReason(record)
  const workspaceLabel =
    record.workspace_name || (record.workspace_id ? wsName(record.workspace_id) : '')

  return (
    <Sheet open={!!record} onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 p-0 sm:max-w-[min(720px,92vw)]"
      >
        <SheetHeader className="space-y-3 border-b border-border/60 p-5 pr-12">
          <div className="flex items-center gap-2">
            <StatusChip status={status} />
            {reason && (
              <span className="font-mono text-xs text-muted-foreground" title={reason}>
                {reason}
              </span>
            )}
            <span className="ml-auto text-xs tabular-nums text-muted-foreground">
              <Timestamp value={record.timestamp} />
            </span>
          </div>
          <SheetTitle className="flex min-w-0 items-start gap-2 font-mono text-base font-semibold leading-snug break-all">
            <span className="min-w-0">{record.tool_name}</span>
            <CopyButton value={record.tool_name} className="-mt-0.5 shrink-0" />
          </SheetTitle>
          {workspaceLabel && (
            <p className="text-xs text-muted-foreground">in {workspaceLabel}</p>
          )}
        </SheetHeader>

        <div className="flex-1 overflow-y-auto px-5 py-4">
          <AuditInspector
            record={record}
            wsName={wsName}
            asName={asName}
            onClose={onClose}
          />
        </div>

        {(onPrev || onNext) && (
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
                    data-testid="audit-detail-prev"
                    aria-label="Previous record"
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
                    data-testid="audit-detail-next"
                    aria-label="Next record"
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
        )}
      </SheetContent>
    </Sheet>
  )
}
