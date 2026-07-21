import { useEffect, useState } from 'react'
import { Check, Clock, X } from 'lucide-react'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { CopyButton } from '@/components/ui/copy-button'
import { cn } from '@/lib/utils'
import type { ToolApproval } from '@/api/types'
import { formatAbsolute, formatRelative } from '@/pages/tasks/task-utils'
import {
  ApproverChip,
  StatusBadge,
  formatTimeRemaining,
  formatWait,
  kindMeta,
  prettyJSON,
  primaryLabel,
  surfaceMeta,
} from './approval-helpers'
import { useResolveApproval } from './use-resolve-approval'

function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        {label}
      </h3>
      {children}
    </section>
  )
}

// Row is a label/value pair. `mono` renders the value as machine output
// with a copy affordance; IDs and paths use it.
function Row({
  label,
  value,
  mono,
  title,
}: {
  label: string
  value?: string | null
  mono?: boolean
  title?: string
}) {
  if (!value) return null
  return (
    <div className="flex items-baseline justify-between gap-4 text-sm">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="flex min-w-0 items-center gap-1 text-right">
        <span className={cn('truncate', mono && 'font-mono text-xs')} title={title ?? value}>
          {value}
        </span>
        {mono && <CopyButton value={value} className="shrink-0" />}
      </span>
    </div>
  )
}

export function ApprovalDetailSheet({
  approval,
  now,
  onOpenChange,
  onResolved,
}: {
  approval: ToolApproval | null
  now: number
  onOpenChange: (open: boolean) => void
  onResolved: () => void
}) {
  return (
    <Sheet open={!!approval} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 sm:max-w-xl">
        {approval && <Body approval={approval} now={now} onResolved={onResolved} />}
      </SheetContent>
    </Sheet>
  )
}

function Body({
  approval: a,
  now,
  onResolved,
}: {
  approval: ToolApproval
  now: number
  onResolved: () => void
}) {
  const surface = surfaceMeta(a.surface)
  const kind = kindMeta(a.kind)
  const pending = a.status === 'pending'
  const remaining = formatTimeRemaining(a.created_at, a.timeout_sec, now)
  const expired = remaining === 'expired'
  const { reason, setReason, resolving, resolve } = useResolveApproval(a.id, onResolved)

  // Same friction contract as PendingCard: Approve is two-step (it lets
  // the tool call run), Deny needs a reason. Both surfaces resolve through
  // the shared hook, so they must gate identically.
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
    <>
      <SheetHeader className="border-b border-border">
        <div className="flex flex-wrap items-center gap-2 pr-8">
          <StatusBadge status={a.status} />
          <Badge label={surface.label} Icon={surface.Icon} />
          {kind && <Badge label={kind.label} Icon={kind.Icon} />}
        </div>
        <SheetTitle className="break-all font-mono text-base">{primaryLabel(a)}</SheetTitle>
        <SheetDescription>
          Requested by {a.request_client_type || 'unknown'}
          {a.request_model ? ` · ${a.request_model}` : ''}
        </SheetDescription>
      </SheetHeader>

      <div className="flex-1 space-y-6 overflow-y-auto p-4">
        {pending && (
          <div
            className={cn(
              'flex items-center gap-2 border px-3 py-2 text-sm tabular-nums',
              expired
                ? 'border-destructive/40 bg-destructive/10 text-destructive'
                : 'border-amber-500/40 bg-amber-500/10 text-amber-300',
            )}
          >
            <Clock className="h-3.5 w-3.5 shrink-0" />
            {expired ? 'Window expired' : `${remaining} left to decide`}
          </div>
        )}

        <Section label="Request">
          <Row label="Workspace" value={a.workspace_name || a.workspace_id} title={a.workspace_id} />
          <Row label="Originating" value={a.originating_workspace} mono />
          <Row label="Surface" value={surface.label} />
          <Row label="Route rule" value={a.route_rule_id} mono />
          <Row label="Downstream" value={a.downstream_server_id} mono />
          <Row label="Auth scope" value={a.auth_scope_id} mono />
          <Row
            label="Requested"
            value={formatRelative(a.created_at)}
            title={formatAbsolute(a.created_at)}
          />
        </Section>

        {a.justification && (
          <Section label="Justification">
            <p className="bg-muted/40 p-3 text-sm leading-relaxed">{a.justification}</p>
          </Section>
        )}

        {a.kind && a.summary && (
          <Section label="Summary">
            <p className="bg-muted/40 p-3 text-sm leading-relaxed">{a.summary}</p>
          </Section>
        )}

        {a.arguments && a.arguments !== '{}' && (
          <Section label="Arguments">
            <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words bg-muted/40 p-3 font-mono text-xs">
              {prettyJSON(a.arguments)}
            </pre>
          </Section>
        )}

        {!pending && (
          <Section label="What happened">
            <div className="flex items-baseline justify-between gap-4 text-sm">
              <span className="text-muted-foreground">Outcome</span>
              <StatusBadge status={a.status} />
            </div>
            <div className="flex items-baseline justify-between gap-4 text-sm">
              <span className="text-muted-foreground">Resolved by</span>
              <ApproverChip approverType={a.approver_type} approverSessionID={a.approver_session_id} />
            </div>
            <Row label="Waited" value={formatWait(a)} />
            <Row
              label="Resolved"
              value={a.resolved_at ? formatRelative(a.resolved_at) : null}
              title={a.resolved_at ? formatAbsolute(a.resolved_at) : undefined}
            />
            {a.resolution && (
              <p className="mt-1 bg-muted/40 p-3 text-sm leading-relaxed">{a.resolution}</p>
            )}
          </Section>
        )}
      </div>

      {pending && (
        <div className="space-y-2 border-t border-border p-4">
          <Input
            placeholder="Reason (required to deny)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            className="text-sm"
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
      )}
    </>
  )
}

// Badge is a tiny mono chip for surface/kind tags inside the sheet header.
// It uses the shared muted/mono tone so it reads as a literal machine
// label, not a status. Kept local since it only serves this header.
function Badge({ label, Icon }: { label: string; Icon: React.ComponentType<{ className?: string }> }) {
  return (
    <span className="inline-flex items-center gap-1 border border-border bg-muted/40 px-2 py-0.5 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
      <Icon className="h-2.5 w-2.5" />
      {label}
    </span>
  )
}
