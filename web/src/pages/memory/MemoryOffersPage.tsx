// MemoryOffersPage — incoming + outgoing memory offers at /memory/shared.
//
// Incoming: cards per pending offer (peer name + truncated id, name + preview,
// received time, Accept/Decline). Live-updates piggy-back on the existing
// Signal stream (memory-related events trigger a refetch). Outgoing is a
// placeholder for the next iteration.

import { useEffect, useMemo } from 'react'
import { Link } from 'react-router-dom'
import {
  ArrowLeft,
  Inbox,
  Send,
  Share2,
  Sparkles,
  Users,
} from 'lucide-react'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { useMemoryOffers } from '@/hooks/use-memory'
import { useSignal } from '@/components/notifications/use-signal'
import type { MemoryOffer } from '@/api/memory'
import { MemoryOfferCard } from './MemoryOfferCard'
import { relativeTime } from './memory-utils'
import { cn } from '@/lib/utils'

export function MemoryOffersPage() {
  const { data: offers, refetch } = useMemoryOffers({
    pending_only: false,
    limit: 100,
  })
  const { events } = useSignal()

  // Live updates: whenever a memory.* signal event arrives, refetch the
  // offers list. Cheap — the endpoint is local and the surface is small.
  const lastEventId = events[0]?.id
  useEffect(() => {
    if (!lastEventId) return
    refetch()
  }, [lastEventId, refetch])

  const incoming = useMemo(
    () => (offers ?? []).filter((o) => !o.accepted_at && !o.declined_at),
    [offers],
  )
  const history = useMemo(
    () =>
      (offers ?? [])
        .filter((o) => o.accepted_at || o.declined_at)
        .slice(0, 20),
    [offers],
  )

  return (
    <div className="space-y-5">
      <Link
        to="/memory"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Memory
      </Link>
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <Share2 className="h-5 w-5 text-primary" />
          Shared memories
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Memories your paired peers have offered to share, and memories you
          have offered to them. Accept to pull the full content into your
          local store.
        </p>
      </header>

      <Tabs defaultValue="incoming" className="w-full">
        <TabsList variant="line" className="border-b border-border">
          <TabsTrigger value="incoming" data-testid="memory-offers-tab-incoming">
            <Inbox className="h-3.5 w-3.5" />
            Incoming
            {incoming.length > 0 && (
              <span className="ml-1.5 inline-flex h-4 min-w-4 items-center justify-center rounded-sm bg-emerald-500/20 px-1 font-mono text-[10px] text-emerald-300">
                {incoming.length}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="outgoing" data-testid="memory-offers-tab-outgoing">
            <Send className="h-3.5 w-3.5" />
            Outgoing
          </TabsTrigger>
          <TabsTrigger value="history" data-testid="memory-offers-tab-history">
            <Sparkles className="h-3.5 w-3.5" />
            History
          </TabsTrigger>
        </TabsList>

        <TabsContent value="incoming" className="space-y-3 pt-5">
          <IncomingPanel offers={incoming} onChange={refetch} />
        </TabsContent>
        <TabsContent value="outgoing" className="pt-5">
          <EmptyState
            icon={<Send className="h-7 w-7" />}
            title="Outgoing offers — coming soon"
            description="The mesh layer will let you offer any local memory to a paired peer with a single click. Track delivery + acceptance status here."
            density="card"
            testid="memory-offers-outgoing-empty"
          />
        </TabsContent>
        <TabsContent value="history" className="space-y-2 pt-5">
          <HistoryPanel offers={history} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function IncomingPanel({
  offers,
  onChange,
}: {
  offers: MemoryOffer[]
  onChange: () => void
}) {
  if (offers.length === 0) {
    return (
      <EmptyState
        icon={<Users className="h-7 w-7" />}
        title="No incoming offers"
        description="When a paired peer shares a memory, it lands here. You stay in control — the gateway never pulls remote content without your explicit accept."
        density="card"
        testid="memory-offers-incoming-empty"
      />
    )
  }
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
      {offers.map((o) => (
        <MemoryOfferCard key={o.id} offer={o} onChange={onChange} />
      ))}
    </div>
  )
}

function HistoryPanel({ offers }: { offers: MemoryOffer[] }) {
  if (offers.length === 0) {
    return (
      <EmptyState
        title="No offer history yet"
        description="Once you accept or decline offers, they will appear here for reference."
        density="card"
      />
    )
  }
  return (
    <ul className="divide-y divide-border/40 border border-border/40 bg-background/40">
      {offers.map((o) => {
        const accepted = !!o.accepted_at
        return (
          <li
            key={o.id}
            className="flex items-center gap-3 px-4 py-2.5"
            data-testid={`memory-offer-history-${o.id}`}
          >
            <span
              className={cn(
                'inline-flex h-1.5 w-1.5 shrink-0 rounded-full',
                accepted ? 'bg-emerald-400' : 'bg-muted-foreground/40',
              )}
            />
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate font-mono text-[12.5px] text-foreground">
                  {o.name}
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
              <p className="text-[11px] text-muted-foreground/70">
                from {o.peer_name || 'unknown'} ·{' '}
                {relativeTime(o.accepted_at || o.declined_at || o.received_at)}
              </p>
            </div>
          </li>
        )
      })}
    </ul>
  )
}
