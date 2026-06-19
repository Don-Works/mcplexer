import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ShieldCheck } from 'lucide-react'
import { EmptyState } from '@/components/ui/empty-state'
import { useApprovalStream } from '@/hooks/use-approval-stream'
import { useApi } from '@/hooks/use-api'
import { listApprovals } from '@/api/client'
import type { ToolApproval } from '@/api/types'
import { PendingCard } from '@/pages/approvals/PendingCard'
import { HistoryList } from '@/pages/approvals/HistoryList'
import { ApprovalDetailSheet } from '@/pages/approvals/ApprovalDetailSheet'

// Tick once per second so pending countdowns stay live without re-fetching.
function useNowTick(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

export function ApprovalsPage() {
  const { pending, connected } = useApprovalStream()
  const now = useNowTick()

  const pendingFetcher = useCallback(() => listApprovals('pending'), [])
  const { data: dbPending, refetch: refetchPending } = useApi(pendingFetcher)

  const historyFetcher = useCallback(() => listApprovals('resolved'), [])
  const { data: history, refetch: refetchHistory } = useApi(historyFetcher)

  const [selected, setSelected] = useState<ToolApproval | null>(null)

  // Merge SSE pending with the initial DB load, dedup by id. SSE is the
  // live source; the DB fetch covers rows that predate this mount.
  const allPending = useMemo(() => {
    const seen = new Set<string>()
    const merged: ToolApproval[] = []
    for (const a of [...pending, ...(dbPending ?? [])]) {
      if (!seen.has(a.id)) {
        seen.add(a.id)
        merged.push(a)
      }
    }
    return merged
  }, [pending, dbPending])

  const handleResolved = useCallback(() => {
    setSelected(null)
    refetchPending()
    refetchHistory()
  }, [refetchPending, refetchHistory])

  // Deep-link target: /approvals?selected=<id> from an OS notification or
  // signal-tray click. Scroll the matching pending card into view and
  // pulse a ring so it's findable in a long list. Clears the param after.
  const [searchParams, setSearchParams] = useSearchParams()
  const targetID = searchParams.get('selected')
  const [highlightID, setHighlightID] = useState<string | null>(null)
  const rowRefs = useRef<Map<string, HTMLDivElement>>(new Map())
  const registerRow = useCallback((id: string, el: HTMLDivElement | null) => {
    if (el) rowRefs.current.set(id, el)
    else rowRefs.current.delete(id)
  }, [])
  useEffect(() => {
    if (!targetID) return
    setHighlightID(targetID)
    let attempts = 0
    const tick = () => {
      const el = rowRefs.current.get(targetID)
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'center' })
        return
      }
      if (++attempts <= 20) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
    const clearParam = setTimeout(() => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          next.delete('selected')
          return next
        },
        { replace: true },
      )
    }, 200)
    const clearHighlight = setTimeout(() => setHighlightID(null), 3500)
    return () => {
      clearTimeout(clearParam)
      clearTimeout(clearHighlight)
    }
  }, [targetID, setSearchParams])

  return (
    <div className="space-y-8">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-bold">Approvals</h1>
        {connected ? (
          <span className="flex items-center gap-1.5 text-xs text-emerald-400">
            <span className="relative flex h-2 w-2">
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
              <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-500" />
            </span>
            Live
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">Connecting...</span>
        )}
      </div>

      {allPending.length > 0 ? (
        <section className="space-y-3">
          <h2 className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
            Pending ({allPending.length})
          </h2>
          <div className="grid gap-4 md:grid-cols-2">
            {allPending.map((a) => (
              <PendingCard
                key={a.id}
                approval={a}
                onResolved={handleResolved}
                now={now}
                highlighted={highlightID === a.id}
                registerRef={registerRow}
                onOpenDetail={() => setSelected(a)}
              />
            ))}
          </div>
        </section>
      ) : (
        <EmptyState
          icon={<ShieldCheck className="h-8 w-8" />}
          title="No pending approvals"
          testid="approvals-empty"
          description={
            <>
              When an agent tries to use a tool that requires your permission, it shows up here
              as a pending request. You can approve or deny each one before it runs.
            </>
          }
        />
      )}

      {(history?.length ?? 0) > 0 && (
        <HistoryList items={history ?? []} onOpenDetail={setSelected} />
      )}

      <ApprovalDetailSheet
        approval={selected}
        now={now}
        onOpenChange={(open) => !open && setSelected(null)}
        onResolved={handleResolved}
      />
    </div>
  )
}
