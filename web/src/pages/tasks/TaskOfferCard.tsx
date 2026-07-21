// TaskOfferCard — single incoming-offer card used by TaskOffersPage.
// Carries the preview-only payload the sender shipped over the wire and
// surfaces the accept/decline controls. On accept the dashboard makes
// a /tasks/offers/{id}/accept call that the daemon turns into a full
// task-payload pull over libp2p; on decline we just stamp the offer.
//
// First-from-peer flow: the daemon rejects accept with 428 until the
// operator picks the local workspace the offer should land in. The
// card renders an inline workspace picker for that case and resubmits
// once a choice is made.

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { CheckCircle2, Loader2, ShieldAlert, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ApiClientError } from '@/api/client'
import { acceptTaskOffer, declineTaskOffer, type TaskOffer } from '@/api/tasks'
import type { Workspace } from '@/api/types'
import { cn } from '@/lib/utils'

function shortPeer(id: string): string {
  if (id.length <= 14) return id
  return `${id.slice(0, 10)}…${id.slice(-4)}`
}

function relative(iso: string): string {
  const d = Date.now() - new Date(iso).getTime()
  const mins = Math.floor(d / 60_000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

interface Props {
  offer: TaskOffer
  workspaces: Workspace[]
  onChange: () => void
}

export function TaskOfferCard({ offer, workspaces, onChange }: Props) {
  const [busy, setBusy] = useState<'accept' | 'decline' | null>(null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [pickedWorkspace, setPickedWorkspace] = useState('')

  async function attemptAccept(workspaceId?: string) {
    setBusy('accept')
    try {
      const created = await acceptTaskOffer(offer.id, workspaceId)
      toast.success(`Accepted "${created.title}"`)
      setPickerOpen(false)
      onChange()
    } catch (err) {
      if (err instanceof ApiClientError && err.status === 428) {
        // First offer from this peer/workspace — daemon needs an explicit
        // local workspace before it can land the row. Surface the picker.
        setPickerOpen(true)
      } else {
        toast.error(err instanceof Error ? err.message : 'Accept failed')
      }
    } finally {
      setBusy(null)
    }
  }

  async function handleDecline() {
    setBusy('decline')
    try {
      await declineTaskOffer(offer.id)
      toast.success('Offer declined')
      onChange()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Decline failed')
    } finally {
      setBusy(null)
    }
  }

  const tags = Array.isArray(offer.tags) ? offer.tags : []
  const remoteWs = offer.remote_workspace_name || offer.remote_workspace_id
  const preview = offer.description_preview?.trim() || offer.meta_preview?.trim() || ''

  return (
    <Card
      className={cn(
        'overflow-hidden border-sky-500/25',
        offer.is_direct_assign && 'border-amber-500/40',
      )}
      data-testid={`task-offer-${offer.id}`}
    >
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-1.5">
              {offer.is_direct_assign ? (
                <Badge variant="outline" tone="muted" className="gap-1 border-amber-500/30 text-amber-300">
                  <ShieldAlert className="h-3 w-3" />
                  direct assign
                </Badge>
              ) : (
                <Badge variant="outline" tone="muted">offer</Badge>
              )}
              {offer.status_preview && (
                <Badge variant="outline" tone="muted" className="font-mono text-[10px] uppercase">
                  {offer.status_preview}
                </Badge>
              )}
              {offer.priority_preview && offer.priority_preview !== 'normal' && (
                <Badge variant="outline" tone="muted" className="font-mono text-[10px] uppercase">
                  {offer.priority_preview}
                </Badge>
              )}
            </div>
            <p
              className="font-mono text-[10.5px] text-muted-foreground/70"
              title={offer.from_peer_id}
            >
              from <span className="text-foreground/70">{shortPeer(offer.from_peer_id)}</span>
              {remoteWs && (
                <>
                  {' · '}
                  workspace <span className="text-foreground/70">{remoteWs}</span>
                </>
              )}
            </p>
          </div>
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70">
            {relative(offer.envelope_created_at || offer.created_at)}
          </span>
        </div>

        <div className="space-y-1.5">
          <p className="break-words font-mono text-[13px] font-medium text-foreground">
            {offer.title}
          </p>
          {preview && (
            <p className="line-clamp-3 whitespace-pre-wrap text-[12.5px] leading-relaxed text-muted-foreground/90">
              {preview}
            </p>
          )}
        </div>

        {tags.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {tags.slice(0, 6).map((t) => (
              <Badge key={t} variant="outline" tone="muted" className="font-mono text-[10px]">
                {t}
              </Badge>
            ))}
            {tags.length > 6 && (
              <span className="text-[10px] text-muted-foreground/60">
                +{tags.length - 6}
              </span>
            )}
          </div>
        )}

        {pickerOpen && (
          <div className="space-y-2 border border-amber-500/30 bg-amber-500/5 p-3">
            <p className="text-[11.5px] leading-relaxed text-amber-200/90">
              First offer from this peer/workspace pair — pick the local
              workspace it should land in. The choice is remembered for
              future offers from the same source.
            </p>
            <Select value={pickedWorkspace} onValueChange={setPickedWorkspace}>
              <SelectTrigger className="h-8 text-[12px]">
                <SelectValue placeholder="Choose local workspace…" />
              </SelectTrigger>
              <SelectContent>
                {workspaces.map((w) => (
                  <SelectItem key={w.id} value={w.id}>
                    {w.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <div className="flex justify-end gap-2">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setPickerOpen(false)}
                disabled={busy !== null}
              >
                Cancel
              </Button>
              <Button
                size="sm"
                disabled={busy !== null || !pickedWorkspace}
                onClick={() => void attemptAccept(pickedWorkspace)}
              >
                {busy === 'accept' ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <CheckCircle2 className="h-3.5 w-3.5" />
                )}
                Accept into workspace
              </Button>
            </div>
          </div>
        )}

        {!pickerOpen && (
          <div className="flex items-center justify-end gap-2 border-t border-border/30 pt-3">
            <Button
              variant="outline"
              size="sm"
              disabled={busy !== null}
              onClick={handleDecline}
              className="gap-1.5 border-destructive/30 text-destructive hover:bg-destructive/5"
              data-testid={`task-offer-${offer.id}-decline`}
            >
              {busy === 'decline' ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <XCircle className="h-3.5 w-3.5" />
              )}
              Decline
            </Button>
            <Button
              variant="default"
              size="sm"
              disabled={busy !== null}
              onClick={() => void attemptAccept(undefined)}
              className="gap-1.5"
              data-testid={`task-offer-${offer.id}-accept`}
            >
              {busy === 'accept' ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <CheckCircle2 className="h-3.5 w-3.5" />
              )}
              Accept
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// TaskOfferHistoryRow — compact accepted/declined row for the History
// tab. Links into the local task when the daemon recorded one.
export function TaskOfferHistoryRow({ offer }: { offer: TaskOffer }) {
  const accepted = !!offer.accepted_at
  const peer = shortPeer(offer.from_peer_id)
  const when = offer.accepted_at || offer.declined_at || offer.created_at
  const body = (
    <>
      <span
        className={cn(
          'inline-flex h-1.5 w-1.5 shrink-0 rounded-full',
          accepted ? 'bg-emerald-400' : 'bg-muted-foreground/40',
        )}
      />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[12.5px] text-foreground">
            {offer.title}
          </span>
          <span
            className={cn(
              'font-mono text-[9px] uppercase tracking-wider',
              accepted
                ? 'text-emerald-300/80'
                : 'text-muted-foreground/60',
            )}
          >
            {accepted ? 'accepted' : 'declined'}
          </span>
        </div>
        <p className="text-[10.5px] text-muted-foreground/70">
          from {peer} · {relative(when)}
          {offer.declined_reason ? ` · ${offer.declined_reason}` : ''}
        </p>
      </div>
    </>
  )

  if (accepted && offer.task_id && offer.workspace_id) {
    return (
      <Link
        to={`/tasks/${encodeURIComponent(offer.task_id)}?workspace=${encodeURIComponent(offer.workspace_id)}`}
        className="flex items-center gap-3 px-4 py-2.5 transition-colors hover:bg-muted/30"
        data-testid={`task-offer-history-${offer.id}`}
      >
        {body}
      </Link>
    )
  }
  return (
    <div
      className="flex items-center gap-3 px-4 py-2.5"
      data-testid={`task-offer-history-${offer.id}`}
    >
      {body}
    </div>
  )
}
