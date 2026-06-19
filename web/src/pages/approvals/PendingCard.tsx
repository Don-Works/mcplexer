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

          <p className="line-clamp-2 bg-amber-500/[0.06] p-2 text-sm leading-relaxed text-foreground/90">
            {a.justification || a.summary || 'No justification provided'}
          </p>
        </button>

        <div className="space-y-2">
          <Input
            placeholder="Reason (required for deny)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            className="text-sm"
            data-testid={`approval-reason-${a.id}`}
            aria-label="Approval reason"
          />
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="success"
              onClick={() => resolve(true)}
              disabled={resolving || expired}
              className="flex-1"
              data-testid={`approval-approve-${a.id}`}
            >
              <Check className="mr-1 h-3.5 w-3.5" />
              Approve
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => resolve(false)}
              disabled={resolving || expired}
              className="border-destructive/40 text-destructive hover:bg-destructive/10 hover:text-destructive"
              data-testid={`approval-deny-${a.id}`}
            >
              <X className="mr-1 h-3.5 w-3.5" />
              Deny
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
