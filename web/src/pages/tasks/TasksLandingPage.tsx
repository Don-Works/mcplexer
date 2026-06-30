// TasksLandingPage — the calm overview of /tasks. Parallels
// MemoryLandingPage in shape: vitals strip, live activity, quick
// links, by-workspace breakdown. The deep list moved to /tasks/all
// so this surface can answer "what's the gateway working on right
// now" without overwhelming the operator with rows.
//
// Presentational tiles + the activity card live in TasksLandingTiles
// to keep this file under the 300-line guideline.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, useSearchParams } from 'react-router-dom'
import { Activity, Inbox, ListTodo } from 'lucide-react'

import { listWorkspaces } from '@/api/client'
import { listTasks, listTaskOffers, listTaskStatuses, type Task } from '@/api/tasks'
import { useApi } from '@/hooks/use-api'
import { useTasksStream } from '@/hooks/use-tasks-stream'
import { useRecentTaskActivity } from '@/hooks/use-recent-task-activity'
import { isWorkingStatus } from './task-utils'
import { buildTaskHistoryEvents, mergeTaskActivityEvents } from './task-activity'
import {
  ActivityCard,
  NextUpRow,
  QuickLink,
  VitalsTile,
  WorkspaceBreakdown,
} from './TasksLandingTiles'

const NEXT_UP_LIMIT = 5

export function TasksLandingPage() {
  const [params] = useSearchParams()

  // Preserve the focus-by-id contract that TaskRef relies on
  // (`/tasks?focus=<id>` is the fallback when no workspace is known).
  // The list page resolves focus → detail navigation, so we forward
  // the same query string verbatim.
  const focusedId = params.get('focus')
  if (focusedId) {
    return <Navigate to={`/tasks/all?${params.toString()}`} replace />
  }

  return <TasksLandingPageBody />
}

function TasksLandingPageBody() {
  const tasksFetcher = useCallback(
    () => listTasks({ state: 'open', limit: 500 }),
    [],
  )
  const { data: tasks, refetch } = useApi<Task[]>(tasksFetcher)
  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)
  const offersFetcher = useCallback(
    () =>
      listTaskOffers({ direction: 'incoming', state: 'pending', limit: 100 }),
    [],
  )
  const { data: offers, refetch: refetchOffers } = useApi(offersFetcher)

  // True open/doing totals — grouped COUNT(*) across all workspaces, uncapped.
  // The task list below is page-limited to 500 server-side, so its length lies
  // (saturates to a fake "500") the moment the board is busier than one page.
  const statusFetcher = useCallback(() => listTaskStatuses({ state: 'open' }), [])
  const { data: openStatusCounts, refetch: refetchStatusCounts } =
    useApi(statusFetcher)

  const { events, push } = useRecentTaskActivity()
  const [liveCount, setLiveCount] = useState(0)
  useTasksStream({
    onEvent: (evt) => {
      push(evt)
      setLiveCount((n) => Math.min(n + 1, 50))
      refetch()
      refetchOffers()
      refetchStatusCounts()
    },
  })

  // Light fallback poll so the page stays current when the SSE stream
  // is asleep. 30s matches the nav badge / memory landing cadence.
  useEffect(() => {
    const id = window.setInterval(() => {
      refetch()
      refetchOffers()
      refetchStatusCounts()
    }, 30_000)
    return () => window.clearInterval(id)
  }, [refetch, refetchOffers, refetchStatusCounts])

  const workspaceNameByID = useMemo(() => {
    const m: Record<string, string> = {}
    for (const w of workspaces ?? []) m[w.id] = w.name
    return m
  }, [workspaces])

  const allTasks = useMemo(() => tasks ?? [], [tasks])
  const historyEvents = useMemo(
    () => buildTaskHistoryEvents(allTasks),
    [allTasks],
  )
  const activityEvents = useMemo(
    () => mergeTaskActivityEvents(events, historyEvents),
    [events, historyEvents],
  )
  const openTasks = allTasks
  const doingTasks = useMemo(
    () => openTasks.filter((t) => isWorkingStatus(t.status)),
    [openTasks],
  )
  const nextUp = useMemo(() => {
    const priorityRank: Record<string, number> = {
      critical: 0,
      high: 1,
      normal: 2,
      low: 3,
    }
    return [...openTasks]
      .sort((a, b) => {
        const pa = priorityRank[a.priority] ?? 9
        const pb = priorityRank[b.priority] ?? 9
        if (pa !== pb) return pa - pb
        return (b.updated_at || '').localeCompare(a.updated_at || '')
      })
      .slice(0, NEXT_UP_LIMIT)
  }, [openTasks])

  const pendingOffers = offers?.length ?? 0
  // Prefer the uncapped grouped count; fall back to the (capped) list length
  // only until the count request lands, so the tile never flashes a stale 0.
  const statusRows = openStatusCounts?.statuses
  const openCount = useMemo(
    () =>
      statusRows
        ? statusRows.reduce((sum, r) => sum + r.count, 0)
        : openTasks.length,
    [statusRows, openTasks.length],
  )
  const doingCount = useMemo(
    () =>
      statusRows
        ? statusRows
            .filter((r) => isWorkingStatus(r.status))
            .reduce((sum, r) => sum + r.count, 0)
        : doingTasks.length,
    [statusRows, doingTasks.length],
  )

  return (
    <div className="space-y-6">
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2.5 text-2xl font-semibold tracking-tight">
          <ListTodo className="h-5 w-5 text-primary" />
          Tasks
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Operational work items per workspace. Agents create them as they
          plan, claim them as they work, and close them as they ship. You
          triage what matters; the rest stays out of the way.
        </p>
      </header>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        <VitalsTile
          icon={<ListTodo className="h-4 w-4" />}
          label="Open"
          value={String(openCount)}
          detail={
            openCount === 0
              ? 'nothing in flight'
              : `${openCount} task${openCount === 1 ? '' : 's'} on the board`
          }
          href="/tasks/all"
        />
        <VitalsTile
          icon={<Activity className="h-4 w-4" />}
          label="Doing now"
          value={String(doingCount)}
          detail={doingCount > 0 ? 'agents actively working' : 'no agents busy'}
          accent={doingCount > 0 ? 'live' : 'idle'}
          href="/tasks/all?status=doing"
        />
        <VitalsTile
          icon={<Inbox className="h-4 w-4" />}
          label="Incoming offers"
          value={String(pendingOffers)}
          detail={pendingOffers > 0 ? 'awaiting your review' : 'all caught up'}
          accent={pendingOffers > 0 ? 'awaiting' : 'idle'}
          href="/tasks/offers"
        />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ActivityCard
            events={activityEvents}
            workspaceNameByID={workspaceNameByID}
            liveCount={liveCount}
          />
        </div>
        <div className="flex flex-col gap-3">
          <div className="border border-border bg-card/40">
            <div className="border-b border-border/40 px-3 py-2">
              <h2 className="text-[12px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                Next up
              </h2>
            </div>
            {nextUp.length === 0 ? (
              <p className="px-3 py-4 text-[12px] text-muted-foreground">
                Nothing open right now.
              </p>
            ) : (
              <ul className="divide-y divide-border/30">
                {nextUp.map((t) => (
                  <li key={t.id}>
                    <NextUpRow task={t} />
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </div>

      <WorkspaceBreakdown
        tasks={openTasks}
        workspaceNameByID={workspaceNameByID}
      />

      <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
        <QuickLink
          to="/tasks/all"
          title="Browse all tasks"
          body="Search, filter, bulk-act on every task across every workspace."
        />
        <QuickLink
          to="/tasks/offers"
          title="Shared tasks"
          body={
            pendingOffers > 0
              ? `${pendingOffers} peer offer${pendingOffers === 1 ? '' : 's'} awaiting review`
              : 'Tasks offered by paired peers land here.'
          }
          accent={pendingOffers > 0}
        />
        <QuickLink
          to="/tasks/all?tag=milestone"
          title="Milestones"
          body="Epics with a due date and a children rollup — the burndown view."
        />
      </div>
    </div>
  )
}
