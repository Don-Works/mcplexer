// WorkerApprovalsPage — global pending-approval inbox plus a "recently
// resolved" recap so the section feels like an active workflow, not a
// silent empty page when there's nothing pending.

import { useCallback, useMemo } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Check, Loader2, X } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useApi } from '@/hooks/use-api'
import {
  listWorkerApprovals,
  listWorkers,
  type WorkerApproval,
} from '@/api/workers'
import { ApprovalsCard } from './WorkerApprovalsCard'
import { relativeTime } from './worker-utils'

export function WorkerApprovalsPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const runIdFilter = searchParams.get('run_id')

  const pendingFetcher = useCallback(() => listWorkerApprovals({ status: 'pending' }), [])
  const { data: pending, loading, error, refetch } = useApi(pendingFetcher)

  // UI-9 — render the worker's name instead of a UUID. Fetch the
  // workers list once and build an id → name map; reused by every link
  // in the page (recap + grouped sections).
  const workersFetcher = useCallback(() => listWorkers(), [])
  const { data: workers } = useApi(workersFetcher)
  const workerNameById = useMemo(() => {
    const m = new Map<string, string>()
    for (const w of workers ?? []) m.set(w.id, w.name)
    return m
  }, [workers])

  const clearRunFilter = useCallback(() => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        next.delete('run_id')
        return next
      },
      { replace: true },
    )
  }, [setSearchParams])

  const approvedFetcher = useCallback(
    () => listWorkerApprovals({ status: 'approved', limit: 25 }),
    [],
  )
  const { data: approved } = useApi(approvedFetcher)
  const rejectedFetcher = useCallback(
    () => listWorkerApprovals({ status: 'rejected', limit: 25 }),
    [],
  )
  const { data: rejected } = useApi(rejectedFetcher)

  const recap = useMemo(
    () => computeRecap(approved ?? [], rejected ?? []),
    [approved, rejected],
  )

  if (loading && !pending) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> Loading approvals…
      </div>
    )
  }
  if (error && !pending) {
    return (
      <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
        {error}
      </div>
    )
  }
  const allPending = pending ?? []
  const approvals = runIdFilter
    ? allPending.filter((a) => a.run_id === runIdFilter)
    : allPending
  return (
    <div className="space-y-5 max-w-4xl">
      <header>
        <h1 className="text-2xl font-bold tracking-tight">Worker approvals</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Pending propose-mode write-tool dispatches across every worker.
          Approving fires a new run with the tool pre-cleared.
        </p>
      </header>

      {runIdFilter && (
        <div className="flex items-center gap-2 rounded-md border border-border/60 bg-muted/40 px-3 py-2 text-xs">
          <span className="text-muted-foreground">Filtered to run</span>
          <code className="truncate font-mono text-foreground" title={runIdFilter}>{runIdFilter}</code>
          <button
            type="button"
            onClick={clearRunFilter}
            className="ml-auto inline-flex h-5 items-center gap-0.5 rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            title="Clear filter"
          >
            <X className="h-3 w-3" />
            <span>clear</span>
          </button>
        </div>
      )}

      {approvals.length === 0 ? (
        <EmptyWithRecap recap={recap} workerNameById={workerNameById} />
      ) : (
        <GroupedByWorker approvals={approvals} onResolved={refetch} workerNameById={workerNameById} />
      )}

      {/* Always show the recap below pending — context never hurts. */}
      {approvals.length > 0 && recap.items.length > 0 && (
        <RecentlyResolved recap={recap} workerNameById={workerNameById} />
      )}
    </div>
  )
}

interface Recap {
  approved: number
  rejected: number
  items: WorkerApproval[]
}

// computeRecap is intentionally module-scope (not inline in useMemo) so
// the lint's "no Date.now() in render" rule sees a pure function and
// doesn't flag the wall-clock read.
function computeRecap(approved: WorkerApproval[], rejected: WorkerApproval[]): Recap {
  const cutoff = Date.now() - 24 * 60 * 60 * 1000
  const within = (a: WorkerApproval) =>
    a.decided_at && new Date(a.decided_at).getTime() >= cutoff
  const approvedRecent = approved.filter(within)
  const rejectedRecent = rejected.filter(within)
  const all = [...approvedRecent, ...rejectedRecent].sort((a, b) => {
    const at = a.decided_at ? new Date(a.decided_at).getTime() : 0
    const bt = b.decided_at ? new Date(b.decided_at).getTime() : 0
    return bt - at
  })
  return {
    approved: approvedRecent.length,
    rejected: rejectedRecent.length,
    items: all.slice(0, 5),
  }
}

function EmptyWithRecap({ recap, workerNameById }: { recap: Recap; workerNameById: Map<string, string> }) {
  if (recap.items.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">All caught up</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          No pending approvals. Workers in propose mode stop here when
          they try to call a write-class tool; you'll see them listed
          in real time once an approval lands.
        </CardContent>
      </Card>
    )
  }
  return (
    <Card>
      <CardHeader className="space-y-1">
        <CardTitle className="text-base">All caught up</CardTitle>
        <p className="text-xs text-muted-foreground">
          Last 24h:{' '}
          <span className="font-medium text-emerald-400">{recap.approved} approved</span>
          {' · '}
          <span className="font-medium text-destructive">{recap.rejected} rejected</span>
        </p>
      </CardHeader>
      <CardContent>
        <RecapList items={recap.items} workerNameById={workerNameById} />
      </CardContent>
    </Card>
  )
}

function RecentlyResolved({ recap, workerNameById }: { recap: Recap; workerNameById: Map<string, string> }) {
  return (
    <Card className="border-border/60">
      <CardHeader>
        <CardTitle className="text-sm text-muted-foreground">
          Recently resolved
          <span className="ml-2 text-[10px] font-normal">
            {recap.approved} approved · {recap.rejected} rejected (last 24h)
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <RecapList items={recap.items} workerNameById={workerNameById} />
      </CardContent>
    </Card>
  )
}

function RecapList({ items, workerNameById }: { items: WorkerApproval[]; workerNameById: Map<string, string> }) {
  return (
    <ul className="divide-y divide-border/60">
      {items.map((a) => (
        <li key={a.id} className="flex items-center gap-3 py-2 text-xs">
          {a.status === 'approved' ? (
            <Check className="h-3.5 w-3.5 shrink-0 text-emerald-400" />
          ) : (
            <X className="h-3.5 w-3.5 shrink-0 text-destructive" />
          )}
          <Badge variant="outline" className="font-mono text-[10px]">
            {a.tool_name}
          </Badge>
          <Link
            to={`/workers/${encodeURIComponent(a.worker_id)}`}
            className="truncate text-muted-foreground hover:text-foreground"
          >
            {workerNameById.get(a.worker_id) ?? `worker ${a.worker_id}`}
          </Link>
          <span className="ml-auto text-[10px] text-muted-foreground/60">
            {relativeTime(a.decided_at)}
          </span>
        </li>
      ))}
    </ul>
  )
}

interface GroupedProps {
  approvals: WorkerApproval[]
  onResolved: () => void
  workerNameById: Map<string, string>
}

function GroupedByWorker({ approvals, onResolved, workerNameById }: GroupedProps) {
  const groups = new Map<string, WorkerApproval[]>()
  for (const a of approvals) {
    const arr = groups.get(a.worker_id) ?? []
    arr.push(a)
    groups.set(a.worker_id, arr)
  }
  return (
    <div className="space-y-4">
      {Array.from(groups.entries()).map(([workerID, batch]) => (
        <section key={workerID} className="space-y-2">
          <Link
            to={`/workers/${encodeURIComponent(workerID)}`}
            className="text-xs font-medium text-muted-foreground hover:text-foreground"
          >
            {workerNameById.get(workerID) ?? `Worker ${workerID}`} →
          </Link>
          <ApprovalsCard approvals={batch} onResolved={onResolved} />
        </section>
      ))}
    </div>
  )
}
