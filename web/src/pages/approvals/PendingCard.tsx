import { useEffect, useState } from 'react'
import { Check, Clock, X } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import type { ToolApproval } from '@/api/types'
import { formatTimeRemaining, kindMeta, primaryLabel, surfaceMeta } from './approval-helpers'
import { useResolveApproval } from './use-resolve-approval'

// PendingCard is the action surface: it shows just enough to decide
// (what, who asked, why, how long left) with inline approve/deny for the
// fast path. The card body is a button that opens the full detail drawer
// for arguments, routing, and the complete request envelope.
export function PendingCard({
  approval: a,
  onResolved,
  now,
  highlighted,
  registerRef,
  onOpenDetail,
}: {
  approval: ToolApproval
  onResolved: () => void
  now: number
  highlighted?: boolean
  registerRef?: (id: string, el: HTMLDivElement | null) => void
  onOpenDetail: () => void
}) {
  const { reason, setReason, resolving, resolve } = useResolveApproval(a.id, onResolved)
  const remaining = formatTimeRemaining(a.created_at, a.timeout_sec, now)
  const expired = remaining === 'expired'
  const surface = surfaceMeta(a.surface)
  const kind = kindMeta(a.kind)

  // Approve is the risky action on a security gate: it lets the tool call
  // run. It carries deliberate friction (a two-step confirm) so an
  // operator can't rubber-stamp on reflex. Deny carries its own friction
  // (a required reason). The arm auto-cancels after a few seconds so a
  // stray first click never leaves the card primed to approve.
  const [armed, setArmed] = useState(false)
  useEffect(() => {
    if (!armed) return
    const id = window.setTimeout(() => setArmed(false), 4000)
    return () => window.clearTimeout(id)
  }, [armed])

  const denyReady = reason.trim().length > 0

  function handleApprove() {
    if (!armed) {
      setArmed(true)
      return
    }
    setArmed(false)
    resolve(true)
  }

  return (
    <Card
      ref={(el) => registerRef?.(a.id, el as HTMLDivElement | null)}
      className={cn(
        'border-amber-500/40 bg-amber-500/[0.03] transition-shadow',
        highlighted && 'ring-2 ring-amber-400/80 ring-offset-2 ring-offset-background',
      )}
    >
      <CardContent className="space-y-3 pt-5">
        <button
          type="button"
          onClick={onOpenDetail}
          data-testid={`approval-detail-${a.id}`}
          className="-m-1 block w-full space-y-2 p-1 text-left transition-colors hover:bg-amber-500/[0.04]"
        >
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 space-y-1">
              <div className="break-all font-mono text-sm text-accent-foreground">
                {primaryLabel(a)}
              </div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                <span className="inline-flex items-center gap-1 font-mono uppercase tracking-wider">
                  <surface.Icon className="h-2.5 w-2.5" />
                  {surface.label}
                </span>
                {kind && (
                  <span className="inline-flex items-center gap-1 font-mono uppercase tracking-wider">
                    <kind.Icon className="h-2.5 w-2.5" />
                    {kind.label}
                  </span>
                )}
                <span>· {a.request_client_type || 'unknown'}</span>
              </div>
            </div>
            <div
              className={cn(
                'flex shrink-0 items-center gap-1.5 text-xs tabular-nums',
                expired ? 'text-destructive' : 'text-amber-500',
              )}
            >
              <Clock className="h-3 w-3" />
              {remaining}
            </div>
          </div>

          <p className="whitespace-pre-wrap break-words bg-amber-500/[0.06] p-2 text-sm leading-relaxed text-foreground/90">
            {a.justification || a.summary || 'No justification provided'}
          </p>
        </button>

        <div className="space-y-2">
          <Input
            placeholder="Reason (required to deny)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            className="text-sm"
            data-testid={`approval-reason-${a.id}`}
            aria-label="Approval reason"
          />
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={handleApprove}
              disabled={resolving || expired}
              className={cn(
                'flex-1 border-emerald-500/40 text-emerald-300 hover:bg-emerald-500/10 hover:text-emerald-200',
                armed && 'border-emerald-400 bg-emerald-500/15 text-emerald-100',
              )}
              data-testid={`approval-approve-${a.id}`}
            >
              <Check className="mr-1 h-3.5 w-3.5" />
              {armed ? 'Confirm approve' : 'Approve'}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => resolve(false)}
              disabled={resolving || expired || !denyReady}
              className="flex-1 border-destructive/40 text-destructive hover:bg-destructive/10 hover:text-destructive"
              data-testid={`approval-deny-${a.id}`}
            >
              <X className="mr-1 h-3.5 w-3.5" />
              Deny
            </Button>
          </div>
          {armed ? (
            <p className="text-[11px] text-emerald-300/90" role="status">
              Click confirm to approve, or wait to cancel.
            </p>
          ) : !denyReady ? (
            <p className="text-[11px] text-muted-foreground">Denying requires a reason.</p>
          ) : null}
        </div>
      </CardContent>
    </Card>
  )
}
