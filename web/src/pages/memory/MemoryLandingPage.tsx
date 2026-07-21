// MemoryLandingPage — the "crowning jewel" landing for /memory.
//
// Three vitals tiles, a live activity stream, a harness import card,
// and quick links into the deeper memory surfaces. Designed to feel calm
// and inhabited — humans should know at a glance what their gateway is
// learning. No gratuitous animation; pulse-slow for awaiting states only.
//
// Presentational helpers (tiles, activity card, quick links) live in
// MemoryLandingTiles.tsx to keep this page file under 300 lines.

import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, BookOpen, BrainCircuit, Inbox, Workflow } from 'lucide-react'
import { useSignal } from '@/components/notifications/use-signal'
import {
  useConsolidatorSummary,
  useMemoryCount,
  useMemoryOffers,
} from '@/hooks/use-memory'
import { getEmbeddingsStatus, getMemoryConflicts, type EmbeddingsStatus } from '@/api/memory'
import {
  ActivityCard,
  HarnessImportCard,
  QuickLink,
  VitalsTile,
} from './MemoryLandingTiles'
import { MemoryBrainStats } from './MemoryBrainStats'
import { MemoryGetStartedPanel } from './MemoryGetStartedPanel'
import { MemoryTopEntities } from './MemoryTopEntities'
import { isMemoryEvent, relativeTime } from './memory-utils'

export function MemoryLandingPage() {
  const { data: count } = useMemoryCount()
  const { data: offers } = useMemoryOffers({ pending_only: true })
  const { data: consolidator } = useConsolidatorSummary()
  const { events } = useSignal()
  const [embed, setEmbed] = useState<EmbeddingsStatus | null>(null)
  const [conflictCount, setConflictCount] = useState<number | null>(null)
  useEffect(() => {
    void getEmbeddingsStatus().then(setEmbed).catch(() => {})
    void getMemoryConflicts(200)
      .then((o) => setConflictCount(o.conflicts?.length ?? 0))
      .catch(() => {})
  }, [])

  const memoryEvents = useMemo(
    () => events.filter((e) => isMemoryEvent(e.kind, e.source)).slice(0, 15),
    [events],
  )

  const totalMemories = (count?.facts ?? 0) + (count?.notes ?? 0)
  const pendingOffers = offers?.length ?? 0

  // Roll the per-workspace consolidator status into one line for the tile.
  const consolidationDetail = !consolidator
    ? 'loading…'
    : consolidator.lastRunAt
      ? `${consolidator.lastRunStatus ?? 'done'} · ${consolidator.enabled}/${consolidator.total} enabled`
      : consolidator.enabled > 0
        ? 'enabled · no runs yet'
        : 'not enabled'

  return (
    <div className="space-y-6">
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2.5 text-2xl font-semibold tracking-tight">
          <BookOpen className="h-5 w-5 text-primary" />
          Memory
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Persistent, cross-harness facts and notes your agents have learned.
          Survives session boundaries, model swaps, even machines — share with
          paired peers, pull into any MCP-compatible harness.
        </p>
      </header>

      {/* Brain stats header — the "shape of the brain" snapshot. Big
          numerals, sparkline, type donut, recency strip, top tags.
          Replaces the redundant "Memories" tile in the old vitals strip;
          the remaining two (consolidation + offers) keep their nav role
          below for click-through. */}
      <MemoryBrainStats />

      {/* Navigational vitals — operator destinations not surfaced in the
          brain-stats header. */}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-4">
        <VitalsTile
          icon={<AlertTriangle className="h-4 w-4" />}
          label="Conflicts to review"
          value={conflictCount === null ? '—' : String(conflictCount)}
          detail={
            conflictCount === null
              ? 'loading…'
              : conflictCount > 0
                ? 'duplicates / contradictions'
                : 'nothing to review'
          }
          accent={(conflictCount ?? 0) > 0 ? 'awaiting' : 'idle'}
          dim={!conflictCount}
          href="/memory/conflicts"
        />
        <VitalsTile
          icon={<BrainCircuit className="h-4 w-4" />}
          label="Semantic recall"
          value={embed ? (embed.embedder_active ? 'active' : 'keyword-only') : '—'}
          detail={
            embed
              ? embed.embedder_active
                ? `${embed.embedded}/${embed.total} embedded${embed.running ? ' · backfilling' : ''}`
                : 'no vector provider — configure'
              : 'loading…'
          }
          accent={embed && !embed.embedder_active ? 'awaiting' : 'idle'}
          dim={!embed?.embedder_active}
          href="/memory/embeddings"
        />
        <VitalsTile
          icon={<Workflow className="h-4 w-4" />}
          label="Last consolidation"
          value={
            consolidator?.lastRunAt ? relativeTime(consolidator.lastRunAt) : '—'
          }
          detail={consolidationDetail}
          dim={!consolidator?.lastRunAt}
          href="/memory/consolidation"
        />
        <VitalsTile
          icon={<Inbox className="h-4 w-4" />}
          label="Incoming offers"
          value={String(pendingOffers)}
          detail={pendingOffers > 0 ? 'awaiting your review' : 'all caught up'}
          accent={pendingOffers > 0 ? 'awaiting' : 'idle'}
          href="/memory/shared"
        />
      </div>

      {/* Live activity (or get-started) + harness import card. When the
          gateway has zero memories we replace the passive "nothing
          learned yet" empty state with a 3-step active onboarding so
          the operator knows exactly what to do next. */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          {totalMemories === 0 ? (
            <MemoryGetStartedPanel />
          ) : (
            <ActivityCard events={memoryEvents} />
          )}
        </div>
        <HarnessImportCard />
      </div>

      {/* Entity-contextual recall (migration 076) — surfaces what the
          memory store knows ABOUT (tasks, persons, places, peers, …).
          Click-through goes to /memory/about/:kind/:id. */}
      {totalMemories > 0 && <MemoryTopEntities />}

      {/* One affordance, not a trio: the consolidation + offers
          destinations already live as the VitalsTiles above (with live
          counts), so duplicating them here only inflated the decision
          surface. Keep the one destination not surfaced elsewhere. */}
      <QuickLink
        to="/memory/all"
        title="Browse all memories"
        body="Search, filter, and inspect every fact and note; pin what matters or invalidate anything stale."
      />
    </div>
  )
}
