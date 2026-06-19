// TaskOffersPage — incoming + outgoing cross-peer task offers at
// /tasks/offers. Mirrors /memory/shared in shape: one tab per
// direction + a History strip for accepted/declined rows. Live
// updates piggy-back on the existing tasks SSE stream so any
// task_event drives a refetch.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { ArrowLeft, Inbox, Send, Share2, Sparkles, Users } from 'lucide-react'

import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { listWorkspaces } from '@/api/client'
import { listTaskOffers, type TaskOffer } from '@/api/tasks'
import { useApi } from '@/hooks/use-api'
import { useTasksStream } from '@/hooks/use-tasks-stream'
import { cn } from '@/lib/utils'
import { TaskOfferCard, TaskOfferHistoryRow } from './TaskOfferCard'

export function TaskOffersPage() {
  const fetcher = useCallback(() => listTaskOffers({ limit: 200 }), [])
  const { data: offers, refetch } = useApi<TaskOffer[]>(fetcher)
  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)

  useTasksStream({
    onEvent: () => {
      refetch()
    },
  })

  // Light polling fallback so the tab stays current when the SSE
  // stream is asleep (e.g. dashboard left open across a daemon
  // restart). 30s is the same cadence the nav badge uses.
  useEffect(() => {
    const id = window.setInterval(refetch, 30_000)
    return () => window.clearInterval(id)
  }, [refetch])

  // pulseOfferId is passed via Link.state when the operator clicks an
  // offer row in the live activity card. Scroll it into view, flash it
  // on arrival, then scrub the state so back/forward nav doesn't replay
  // the cue.
  const location = useLocation()
  const navigate = useNavigate()
  const [pulseOfferId, setPulseOfferId] = useState<string | null>(null)
  useEffect(() => {
    const targetId =
      typeof location.state === 'object' && location.state !== null
        ? (location.state as { pulseOfferId?: string }).pulseOfferId
        : undefined
    if (!targetId) return
    setPulseOfferId(targetId)
    let attempts = 0
    const tick = () => {
      const el = document.querySelector<HTMLElement>(`[data-offer-id="${targetId}"]`)
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'center' })
        return
      }
      if (++attempts > 20) return
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
    navigate(location.pathname + location.search, { replace: true, state: null })
    const clear = window.setTimeout(() => setPulseOfferId(null), 2400)
    return () => window.clearTimeout(clear)
  }, [location.state, location.pathname, location.search, navigate])

  const all = useMemo(() => offers ?? [], [offers])
  const incoming = useMemo(
    () =>
      all.filter(
        (o) => o.direction === 'incoming' && o.state === 'pending',
      ),
    [all],
  )
  const outgoing = useMemo(
    () =>
      all.filter(
        (o) => o.direction === 'outgoing' && o.state === 'pending',
      ),
    [all],
  )
  const history = useMemo(
    () =>
      all
        .filter((o) => o.state !== 'pending')
        .sort((a, b) => {
          const ta = new Date(a.accepted_at || a.declined_at || a.created_at).getTime()
          const tb = new Date(b.accepted_at || b.declined_at || b.created_at).getTime()
          return tb - ta
        })
        .slice(0, 40),
    [all],
  )

  return (
    <div className="space-y-5">
      <Link
        to="/tasks"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="h-3 w-3" />
        Tasks
      </Link>
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <Share2 className="h-5 w-5 text-primary" />
          Shared tasks
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Tasks your paired peers have offered to share, and tasks you have
          offered to them. Accept to pull the full payload into your local
          store; the daemon remembers which local workspace each peer maps to
          after the first accept.
        </p>
      </header>

      <Tabs defaultValue="incoming" className="w-full">
        <TabsList variant="line" className="border-b border-border">
          <TabsTrigger value="incoming" data-testid="task-offers-tab-incoming">
            <Inbox className="h-3.5 w-3.5" />
            Incoming
            {incoming.length > 0 && (
              <span className="ml-1.5 inline-flex h-4 min-w-4 items-center justify-center rounded-sm bg-sky-500/20 px-1 font-mono text-[10px] text-sky-300">
                {incoming.length}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="outgoing" data-testid="task-offers-tab-outgoing">
            <Send className="h-3.5 w-3.5" />
            Outgoing
            {outgoing.length > 0 && (
              <span className="ml-1.5 inline-flex h-4 min-w-4 items-center justify-center rounded-sm bg-muted px-1 font-mono text-[10px] text-muted-foreground">
                {outgoing.length}
              </span>
            )}
          </TabsTrigger>
          <TabsTrigger value="history" data-testid="task-offers-tab-history">
            <Sparkles className="h-3.5 w-3.5" />
            History
          </TabsTrigger>
        </TabsList>

        <TabsContent value="incoming" className="space-y-3 pt-5">
          {incoming.length === 0 ? (
            <EmptyState
              icon={<Users className="h-7 w-7" />}
              title="No incoming offers"
              description="When a paired peer offers you a task, it lands here. The gateway never pulls the full payload until you explicitly accept."
              density="card"
              testid="task-offers-incoming-empty"
            />
          ) : (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              {incoming.map((o) => (
                <div
                  key={o.id}
                  data-offer-id={o.id}
                  className={cn(
                    'transition-shadow duration-700',
                    pulseOfferId === o.id && 'ring-1 ring-primary/50 shadow-[0_0_0_4px_hsl(205_95%_55%/0.08)]',
                  )}
                >
                  <TaskOfferCard
                    offer={o}
                    workspaces={workspaces ?? []}
                    onChange={refetch}
                  />
                </div>
              ))}
            </div>
          )}
        </TabsContent>

        <TabsContent value="outgoing" className="space-y-3 pt-5">
          {outgoing.length === 0 ? (
            <EmptyState
              icon={<Send className="h-7 w-7" />}
              title="No outgoing offers"
              description="Use task__offer / task__assign_remote from an agent to share a task with a paired peer. Pending offers will appear here until the peer accepts, declines, or the envelope expires."
              density="card"
              testid="task-offers-outgoing-empty"
            />
          ) : (
            <ul className="divide-y divide-border/40 border border-border/40 bg-background/40">
              {outgoing.map((o) => (
                <li
                  key={o.id}
                  data-offer-id={o.id}
                  className={cn(
                    'transition-colors duration-700',
                    pulseOfferId === o.id && 'bg-primary/10 ring-1 ring-primary/40',
                  )}
                >
                  <TaskOfferHistoryRow offer={o} />
                </li>
              ))}
            </ul>
          )}
        </TabsContent>

        <TabsContent value="history" className="space-y-2 pt-5">
          {history.length === 0 ? (
            <EmptyState
              title="No offer history yet"
              description="Once you accept or decline offers, or peers respond to yours, they will appear here for reference."
              density="card"
            />
          ) : (
            <ul className="divide-y divide-border/40 border border-border/40 bg-background/40">
              {history.map((o) => (
                <li
                  key={o.id}
                  data-offer-id={o.id}
                  className={cn(
                    'transition-colors duration-700',
                    pulseOfferId === o.id && 'bg-primary/10 ring-1 ring-primary/40',
                  )}
                >
                  <TaskOfferHistoryRow offer={o} />
                </li>
              ))}
            </ul>
          )}
        </TabsContent>
      </Tabs>
    </div>
  )
}
