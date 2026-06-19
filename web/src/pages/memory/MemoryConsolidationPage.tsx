// MemoryConsolidationPage — surfaces the always-on memory consolidator.
// The daemon auto-installs the consolidator in every workspace at
// startup whenever an api_key auth scope is configured (see
// cmd/mcplexer/consolidator_autoinstall.go) — there is no manual enable
// flow. Each run does TWO scope-preserving passes per workspace:
//   1. global memory  → consolidated notes saved with scope:"global"
//   2. workspace memory → consolidated notes saved with scope:"workspace"
// so consolidated rows are never written back to the wrong scope.
//
// The UI shows the status of every workspace's consolidator side-by-side
// + a Pause / Resume kill-switch + Run Now. No install button, no
// workspace dropdown.

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft, Clock, Globe, Layers, Loader2, Play, Power, Workflow } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { toast } from 'sonner'
import { listWorkspaces } from '@/api/client'
import type { Workspace } from '@/api/types'
import {
  disableConsolidator,
  enableConsolidator,
  getConsolidatorStatus,
  runConsolidatorNow,
  type ConsolidatorStatus,
} from '@/api/memory'

interface WorkspaceConsolidator {
  workspace: Workspace
  status: ConsolidatorStatus | null
  loading: boolean
}

export function MemoryConsolidationPage() {
  const [rows, setRows] = useState<WorkspaceConsolidator[]>([])
  const [topLevelLoading, setTopLevelLoading] = useState(true)
  const [busy, setBusy] = useState<Record<string, '' | 'pause' | 'resume' | 'run'>>({})

  const refetch = useCallback(async () => {
    setTopLevelLoading(true)
    try {
      const wss = await listWorkspaces()
      const statuses = await Promise.all(
        wss.map(async (w) => {
          try {
            const s = await getConsolidatorStatus(w.id)
            return { workspace: w, status: s, loading: false }
          } catch {
            return { workspace: w, status: null, loading: false }
          }
        }),
      )
      setRows(statuses)
    } catch (err) {
      toast.error('Failed to load workspaces: ' + String(err))
    } finally {
      setTopLevelLoading(false)
    }
  }, [])

  useEffect(() => {
    void refetch()
  }, [refetch])

  const setBusyFor = (wsID: string, v: '' | 'pause' | 'resume' | 'run') =>
    setBusy((prev) => ({ ...prev, [wsID]: v }))

  const onPause = async (wsID: string) => {
    setBusyFor(wsID, 'pause')
    try {
      await disableConsolidator(wsID)
      toast.success('Consolidator paused for this workspace')
      await refetch()
    } catch (err) {
      toast.error('Pause failed: ' + String(err))
    } finally {
      setBusyFor(wsID, '')
    }
  }

  const onResume = async (wsID: string) => {
    setBusyFor(wsID, 'resume')
    try {
      await enableConsolidator({ workspace_id: wsID })
      toast.success('Consolidator resumed')
      await refetch()
    } catch (err) {
      toast.error('Resume failed: ' + String(err))
    } finally {
      setBusyFor(wsID, '')
    }
  }

  const onRunNow = async (wsID: string) => {
    setBusyFor(wsID, 'run')
    try {
      const out = await runConsolidatorNow(wsID)
      toast.success(`Run queued (run_id ${out.run_id.slice(0, 12)}…)`)
      await refetch()
    } catch (err) {
      toast.error('Run failed: ' + String(err))
    } finally {
      setBusyFor(wsID, '')
    }
  }

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
          <Workflow className="h-5 w-5 text-primary" />
          Consolidation
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Auto-installed in every workspace. Each scheduled run does TWO scope-preserving
          passes — one against global memory, one against the workspace — so consolidated
          notes are written back to the same scope as their sources. Pinned memories are
          never touched.
        </p>
      </header>

      <Card>
        <CardContent className="space-y-3 p-5">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Globe className="h-3.5 w-3.5" />
            <span>
              Global memory is consolidated automatically by every workspace's
              consolidator — there is no separate global toggle. Pausing one workspace
              doesn't stop another from consolidating global memory.
            </span>
          </div>
        </CardContent>
      </Card>

      {topLevelLoading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading workspace consolidators…
        </div>
      )}

      {!topLevelLoading && rows.length === 0 && (
        <Card>
          <CardContent className="p-6 text-center text-sm text-muted-foreground">
            No workspaces yet. Once you have at least one workspace and an api_key
            auth scope, the consolidator will install itself on the next daemon
            restart.
          </CardContent>
        </Card>
      )}

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        {rows.map((row) => (
          <WorkspaceCard
            key={row.workspace.id}
            row={row}
            busy={busy[row.workspace.id] || ''}
            onPause={() => onPause(row.workspace.id)}
            onResume={() => onResume(row.workspace.id)}
            onRunNow={() => onRunNow(row.workspace.id)}
          />
        ))}
      </div>

      <Card>
        <CardContent className="p-6">
          <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
            What each run does (per workspace)
          </h2>
          <ul className="mt-3 grid grid-cols-1 gap-2 text-[12.5px] text-muted-foreground/90 md:grid-cols-2">
            <li className="flex items-start gap-2">
              <span className="mt-1 inline-flex h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400/80" />
              <span>
                Pass 1: <span className="font-mono text-foreground/80">memory__list(scope:&quot;global_only&quot;)</span>{' '}
                — cluster + consolidate → write back with{' '}
                <span className="font-mono text-foreground/80">scope:&quot;global&quot;</span>.
              </span>
            </li>
            <li className="flex items-start gap-2">
              <span className="mt-1 inline-flex h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400/80" />
              <span>
                Pass 2: <span className="font-mono text-foreground/80">memory__list(scope:&quot;workspace_only&quot;)</span>{' '}
                — same clustering, write back with{' '}
                <span className="font-mono text-foreground/80">scope:&quot;workspace&quot;</span>.
              </span>
            </li>
            <li className="flex items-start gap-2">
              <span className="mt-1 inline-flex h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400/80" />
              <span>
                Original notes are{' '}
                <span className="font-mono text-foreground/80">memory__invalidate</span>d,
                pointing at the consolidated note — never deleted.
              </span>
            </li>
            <li className="flex items-start gap-2">
              <span className="mt-1 inline-flex h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-400/80" />
              <span>
                Skips pinned memories and{' '}
                <span className="font-mono text-foreground/80">kind=fact</span> rows — only
                consolidates notes.
              </span>
            </li>
          </ul>
        </CardContent>
      </Card>
    </div>
  )
}

function WorkspaceCard({
  row,
  busy,
  onPause,
  onResume,
  onRunNow,
}: {
  row: WorkspaceConsolidator
  busy: '' | 'pause' | 'resume' | 'run'
  onPause: () => void
  onResume: () => void
  onRunNow: () => void
}) {
  const { workspace, status } = row
  const tone = !status?.installed
    ? 'muted'
    : status.enabled
      ? 'success'
      : 'warn'
  const label = !status
    ? '—'
    : !status.installed
      ? 'Not installed yet'
      : status.enabled
        ? 'Enabled'
        : 'Paused'
  const lastRun = formatLastRun(status)

  return (
    <Card>
      <CardContent className="space-y-3 p-5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{workspace.name || workspace.id}</div>
            <div className="truncate font-mono text-[10px] text-muted-foreground/60">
              {workspace.id}
            </div>
          </div>
          <Badge variant="outline" tone={tone} className="text-[10px] uppercase tracking-wider">
            {label}
          </Badge>
        </div>

        <div className="grid grid-cols-2 gap-2 border-t border-border/40 pt-3">
          <Stat icon={<Layers className="h-3.5 w-3.5" />} label="Recent runs" value={status?.recent_runs ? String(status.recent_runs) : '—'} />
          <Stat icon={<Clock className="h-3.5 w-3.5" />} label="Last run" value={lastRun.value} hint={lastRun.hint} />
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-border/40 pt-3">
          {status?.installed && status.enabled && (
            <>
              <Button size="sm" onClick={onRunNow} disabled={busy !== ''}>
                {busy === 'run' ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <Play className="mr-1.5 h-3.5 w-3.5" />}
                Run now
              </Button>
              <Button size="sm" variant="ghost" onClick={onPause} disabled={busy !== ''}>
                Pause
              </Button>
            </>
          )}
          {status?.installed && !status.enabled && (
            <Button size="sm" onClick={onResume} disabled={busy !== ''}>
              {busy === 'resume' ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" /> : <Power className="mr-1.5 h-3.5 w-3.5" />}
              Resume
            </Button>
          )}
          {!status?.installed && (
            <span className="text-[11px] text-muted-foreground">
              Daemon will auto-install on next boot when an api_key auth scope exists.
            </span>
          )}
          {status?.worker_id && (
            <Link
              to={`/workers/${status.worker_id}`}
              className="ml-auto text-[11px] text-muted-foreground hover:text-foreground"
            >
              Tune →
            </Link>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function Stat({
  icon,
  label,
  value,
  hint,
}: {
  icon: React.ReactNode
  label: string
  value: string
  hint?: string
}) {
  return (
    <div className="border border-border bg-card/40 px-2.5 py-2">
      <div className="flex items-center gap-1.5 text-[9px] font-semibold uppercase tracking-wider text-muted-foreground/70">
        {icon}
        {label}
      </div>
      <div className="mt-0.5 font-mono text-base tabular-nums text-muted-foreground/80">{value}</div>
      {hint && <div className="text-[9px] text-muted-foreground/60">{hint}</div>}
    </div>
  )
}

function formatLastRun(s: ConsolidatorStatus | null): { value: string; hint: string } {
  if (!s || !s.last_run_at) return { value: 'never', hint: 'awaiting first pass' }
  const d = new Date(s.last_run_at)
  const now = Date.now()
  const diff = now - d.getTime()
  if (diff < 60_000) return { value: 'just now', hint: d.toISOString() }
  if (diff < 3_600_000) return { value: `${Math.floor(diff / 60_000)}m ago`, hint: d.toISOString() }
  if (diff < 86_400_000) return { value: `${Math.floor(diff / 3_600_000)}h ago`, hint: d.toISOString() }
  return { value: `${Math.floor(diff / 86_400_000)}d ago`, hint: d.toISOString() }
}
