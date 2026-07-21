// WorkerApprovalsCard — inline approval surface on WorkerDetailPage.
// For write-class approvals the tool input IS the whole point, so the
// first few lines of the formatted JSON are shown inline by default;
// "Expand" gives the full block.

import { useMemo, useState } from 'react'
import { Loader2, Check, X, ChevronDown, ChevronRight } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  approveWorkerApproval,
  rejectWorkerApproval,
  type WorkerApproval,
} from '@/api/workers'

interface Props {
  approvals: WorkerApproval[]
  onResolved: () => void
}

export function ApprovalsCard({ approvals, onResolved }: Props) {
  if (approvals.length === 0) return null
  return (
    <Card className="border-amber-400/40 bg-amber-50/20 dark:bg-amber-950/10">
      <CardHeader>
        <CardTitle className="text-base">
          Pending approvals
          <span className="ml-2 rounded-full bg-amber-500/20 px-2 py-0.5 text-[10px] font-medium text-amber-700 dark:text-amber-300">
            {approvals.length}
          </span>
        </CardTitle>
        <p className="text-xs text-muted-foreground">
          This worker stopped mid-run before a write-class tool. Approving
          will fire a new run with the tool pre-cleared.
        </p>
      </CardHeader>
      <CardContent className="space-y-3">
        {approvals.map((a) => (
          <ApprovalRow key={a.id} approval={a} onResolved={onResolved} />
        ))}
      </CardContent>
    </Card>
  )
}

interface RowProps {
  approval: WorkerApproval
  onResolved: () => void
}

const INLINE_PREVIEW_LINES = 3

function ApprovalRow({ approval, onResolved }: RowProps) {
  const [busy, setBusy] = useState<'approve' | 'reject' | null>(null)
  const [expanded, setExpanded] = useState(false)

  const { preview, full, hasMore } = useMemo(() => {
    const pretty = prettyJSON(approval.tool_input)
    const lines = pretty.split('\n')
    return {
      full: pretty,
      preview: lines.slice(0, INLINE_PREVIEW_LINES).join('\n'),
      hasMore: lines.length > INLINE_PREVIEW_LINES,
    }
  }, [approval.tool_input])

  async function handle(kind: 'approve' | 'reject') {
    setBusy(kind)
    try {
      if (kind === 'approve') {
        const out = await approveWorkerApproval(approval.id)
        toast.success(
          out.resumed_run_id
            ? `Approved; new run ${out.resumed_run_id} dispatched`
            : 'Approved',
        )
      } else {
        await rejectWorkerApproval(approval.id)
        toast.success('Rejected; run marked rejected')
      }
      onResolved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `${kind} failed`)
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="rounded-md border border-amber-400/30 bg-background/60 p-3 text-sm">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <span className="rounded-md bg-amber-500/15 px-1.5 py-0.5 font-mono text-[11px] text-amber-700 dark:text-amber-300">
              {approval.tool_name}
            </span>
            <span className="text-[10px] text-muted-foreground/70">
              run {approval.run_id}
            </span>
          </div>
          <p className="text-xs text-muted-foreground">{approval.reason}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={() => handle('approve')}
            disabled={busy !== null}
            data-testid="worker-approval-approve"
          >
            {busy === 'approve' ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Check className="mr-1.5 h-3.5 w-3.5" />
            )}
            Approve & resume
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => handle('reject')}
            disabled={busy !== null}
            className="text-destructive hover:bg-destructive/10"
            data-testid="worker-approval-reject"
          >
            {busy === 'reject' ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <X className="mr-1.5 h-3.5 w-3.5" />
            )}
            Reject
          </Button>
        </div>
      </div>
      {/* Inline preview of the tool input — the input IS the decision. */}
      {full && full !== '(empty)' && (
        <div className="mt-2 space-y-1">
          <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
            Tool input
          </div>
          <pre className="rounded-md border border-border/60 bg-background/80 p-2 font-mono text-[10px] whitespace-pre-wrap">
            {expanded ? full : preview}
            {!expanded && hasMore && (
              <span className="text-muted-foreground/60">{'\n  …'}</span>
            )}
          </pre>
          {hasMore && (
            <button
              type="button"
              onClick={() => setExpanded((e) => !e)}
              className="inline-flex items-center gap-1 text-[11px] text-muted-foreground/70 hover:text-foreground"
            >
              {expanded ? (
                <ChevronDown className="h-3 w-3" />
              ) : (
                <ChevronRight className="h-3 w-3" />
              )}
              {expanded ? 'Collapse' : 'Expand full input'}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

function prettyJSON(raw: string): string {
  if (!raw) return '(empty)'
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
