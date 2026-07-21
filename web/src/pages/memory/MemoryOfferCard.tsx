// MemoryOfferCard — single incoming-offer card used by MemoryOffersPage.
// Extracted so the page file stays under the 300-line guideline.

import { useState } from 'react'
import { CheckCircle2, Loader2, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { createMemory, type MemoryOffer } from '@/api/memory'
import { useMemoryMutations } from '@/hooks/use-memory'
import { KindBadge, TagChips } from './memory-primitives'
import { parseTags, relativeTime } from './memory-utils'

export function MemoryOfferCard({
  offer,
  onChange,
}: {
  offer: MemoryOffer
  onChange: () => void
}) {
  const mut = useMemoryMutations()
  const [busy, setBusy] = useState<'accept' | 'decline' | null>(null)

  async function handleAccept() {
    setBusy('accept')
    try {
      // V1 acceptance flow: write a local memory carrying the offered
      // preview + provenance fields (origin_peer_id + remote_id), then
      // mark the offer accepted with the new id. The full content pull
      // happens later via libp2p; for now the preview is the seed and
      // gets reconciled on the next sync.
      const created = await createMemory({
        name: offer.name,
        kind: offer.kind,
        content: offer.preview || offer.description || '',
        tags: parseTags(offer.tags as never),
        metadata: {
          imported_from_peer_id: offer.peer_id,
          imported_from_peer_name: offer.peer_name,
          remote_id: offer.remote_id,
          embed_model: offer.embed_model,
        },
      })
      await mut.acceptOffer(offer.id, created.id)
      toast.success(`Accepted offer from ${offer.peer_name || 'peer'}`)
      onChange()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Accept failed')
    } finally {
      setBusy(null)
    }
  }

  async function handleDecline() {
    setBusy('decline')
    try {
      await mut.declineOffer(offer.id)
      toast.success('Offer declined')
      onChange()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Decline failed')
    } finally {
      setBusy(null)
    }
  }

  const peerLabel = offer.peer_name || 'unknown peer'
  const peerIdShort = offer.peer_id.slice(0, 10) + '…' + offer.peer_id.slice(-4)
  const previewBody = offer.preview || offer.description || ''
  const tags = parseTags(offer.tags as never)

  return (
    <Card
      className="overflow-hidden border-emerald-500/20"
      data-testid={`memory-offer-${offer.id}`}
    >
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <KindBadge kind={offer.kind} />
              <span className="truncate text-[11px] font-medium text-emerald-300">
                from {peerLabel}
              </span>
            </div>
            <p
              className="mt-1 truncate font-mono text-[11px] text-muted-foreground/70"
              title={offer.peer_id}
            >
              {peerIdShort}
            </p>
          </div>
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
            {relativeTime(offer.received_at)}
          </span>
        </div>

        <div className="space-y-1.5">
          <p className="break-all font-mono text-[13px] font-medium text-foreground">
            {offer.name}
          </p>
          {previewBody && (
            <p className="line-clamp-3 text-[12.5px] leading-relaxed text-muted-foreground/90">
              {previewBody}
            </p>
          )}
        </div>

        {tags.length > 0 && <TagChips tags={tags} max={6} />}

        <div className="flex items-center justify-end gap-2 border-t border-border/30 pt-3">
          <Button
            variant="outline"
            size="sm"
            disabled={busy !== null}
            onClick={handleDecline}
            className="gap-1.5 border-destructive/30 text-destructive hover:bg-destructive/5"
            data-testid={`memory-offer-${offer.id}-decline`}
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
            onClick={handleAccept}
            className="gap-1.5"
            data-testid={`memory-offer-${offer.id}-accept`}
          >
            {busy === 'accept' ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <CheckCircle2 className="h-3.5 w-3.5" />
            )}
            Accept
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
