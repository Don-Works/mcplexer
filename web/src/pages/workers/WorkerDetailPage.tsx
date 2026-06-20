// WorkerDetailPage — the cockpit. Top of the page is a vitals strip
// (next-run countdown, runs today, $ today, success rate). When a run
// is in flight, a live tail strip appears underneath ticking elapsed +
// tokens + cost in real time. Approvals + recent-runs sit immediately
// below — the actionable surface. Cost + configuration live further
// down, with config collapsed by default because it's diagnostic, not
// daily-driver.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  ArrowLeft,
  Bot,
  ChevronDown,
  ChevronRight,
  Loader2,
  Pencil,
  Play,
  Share2,
  Trash2,
} from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { CopyButton } from '@/components/ui/copy-button'
import { useApi } from '@/hooks/use-api'
import {
  deleteWorker,
  getWorker,
  listWorkerApprovals,
  pauseWorker,
  resumeWorker,
  runWorkerNow,
  type Worker,
  type WorkerRun,
} from '@/api/workers'
import {
  getWorkerTemplate,
  publishWorkerAsTemplate,
} from '@/api/worker-templates'
import { EnableSwitch } from './WorkersListPage'
import { RunCard } from './WorkerRunCard'
import { CostSparkline } from './WorkerCostSparkline'
import { ConfigView } from './WorkerConfigView'
import { ApprovalsCard } from './WorkerApprovalsCard'
import { WorkerVitalsStrip } from './WorkerVitalsStrip'
import { WorkerLiveTail } from './WorkerLiveTail'
import { WorkerTriggersCard } from './WorkerTriggersCard'
import { shortID, statusBadgeClass } from './worker-utils'

export function WorkerDetailPage() {
  const { id = '' } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const fetcher = useCallback(() => getWorker(id), [id])
  const { data, loading, error, refetch } = useApi(fetcher)
  const approvalsFetcher = useCallback(
    () => listWorkerApprovals({ status: 'pending' }),
    [],
  )
  const { data: allPending, refetch: refetchApprovals } = useApi(approvalsFetcher)
  const pending = useMemo(
    () => (allPending ?? []).filter((a) => a.worker_id === id),
    [allPending, id],
  )
  const refetchAll = useCallback(() => {
    refetch()
    refetchApprovals()
  }, [refetch, refetchApprovals])
  const [busy, setBusy] = useState<string | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)

  // Poll the worker every 4s so the vitals strip + live tail stay in
  // sync as runs progress. Cheap enough at this cadence.
  useEffect(() => {
    if (!id) return
    const handle = window.setInterval(() => refetch(), 4_000)
    return () => window.clearInterval(handle)
  }, [id, refetch])

  async function handlePublish() {
    setBusy('publish')
    try {
      const entry = await publishWorkerAsTemplate(id)
      toast.success(`Published ${entry.name}@v${entry.version}`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Publish failed')
    } finally {
      setBusy(null)
    }
  }

  async function doAction<T>(key: string, fn: () => Promise<T>, ok: string) {
    setBusy(key)
    try {
      await fn()
      toast.success(ok)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Action failed')
    } finally {
      setBusy(null)
    }
  }

  async function handleDelete() {
    setBusy('delete')
    try {
      await deleteWorker(id)
      toast.success('Worker deleted')
      navigate('/workers')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
      setBusy(null)
    }
  }

  if (loading && !data) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> Loading worker…
      </div>
    )
  }
  if (error && !data) {
    return (
      <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
        {error}
      </div>
    )
  }
  if (!data) return null

  const w = data.worker
  const liveRun = data.recent_runs.find((r) => r.status === 'running') ?? null

  return (
    <div className="space-y-5 max-w-5xl">
      <Header
        worker={w}
        busy={busy}
        onToggle={() =>
          doAction(
            'enable',
            () => (w.enabled ? pauseWorker(w.id) : resumeWorker(w.id)),
            w.enabled
              ? liveRun
                ? 'Worker paused; active run cancelling'
                : 'Worker paused'
              : 'Worker resumed',
          )
        }
        onRunNow={() =>
          doAction('run', () => runWorkerNow(w.id), `Run started for ${w.name}`)
        }
        onPublish={handlePublish}
        onDelete={() => setDeleteOpen(true)}
      />

      <WorkerVitalsStrip worker={w} runs={data.recent_runs} />

      {liveRun && <WorkerLiveTail liveRun={liveRun} />}

      <AutoPausedBanner worker={w} />
      <TemplateUpdateBanner worker={w} />
      <ApprovalsCard approvals={pending} onResolved={refetchAll} />
      <WorkerTriggersCard workerID={w.id} workerName={w.name} />
      <RecentRunsCard runs={data.recent_runs} />
      <CostCard runs={data.recent_runs} />
      <ConfigCard worker={w} />

      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={`Delete worker "${w.name}"?`}
        description="This removes the worker config. Run history is preserved."
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={handleDelete}
      />
    </div>
  )
}

interface HeaderProps {
  worker: Worker
  busy: string | null
  onToggle: () => void
  onRunNow: () => void
  onPublish: () => void
  onDelete: () => void
}

function Header({ worker, busy, onToggle, onRunNow, onPublish, onDelete }: HeaderProps) {
  return (
    <div className="space-y-2">
      <Link
        to="/workers"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Workers
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <Bot className="h-6 w-6" /> {worker.name}
          </h1>
          {worker.description && (
            <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
              {worker.description}
            </p>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
            <Badge
              variant="outline"
              className={statusBadgeClass(worker.enabled ? 'success' : 'paused')}
            >
              {worker.enabled ? 'enabled' : 'paused'}
            </Badge>
            <span
              className="group inline-flex items-center gap-1 font-mono text-[10px] text-muted-foreground/70"
              title={worker.id}
            >
              {shortID(worker.id)}
              <CopyButton value={worker.id} className="h-4 w-4 opacity-0 transition-opacity group-hover:opacity-100" />
            </span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <EnableSwitch
            enabled={worker.enabled}
            busy={busy === 'enable'}
            onToggle={onToggle}
          />
          <Button
            size="sm"
            variant="outline"
            disabled={!worker.enabled || busy !== null}
            onClick={onRunNow}
            data-testid="worker-detail-run-now"
          >
            {busy === 'run' ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Play className="mr-1.5 h-3.5 w-3.5" />
            )}
            Run now
          </Button>
          <Button size="sm" variant="outline" asChild>
            <Link to={`/workers/${worker.id}/edit`}>
              <Pencil className="mr-1.5 h-3.5 w-3.5" /> Edit
            </Link>
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={busy !== null}
            onClick={onPublish}
            data-testid="worker-detail-publish-template"
          >
            {busy === 'publish' ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Share2 className="mr-1.5 h-3.5 w-3.5" />
            )}
            Publish as template
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="text-destructive hover:bg-destructive/10"
            onClick={onDelete}
            data-testid="worker-detail-delete"
          >
            <Trash2 className="mr-1.5 h-3.5 w-3.5" /> Delete
          </Button>
        </div>
      </div>
    </div>
  )
}

function RecentRunsCard({ runs }: { runs: WorkerRun[] }) {
  const [showAll, setShowAll] = useState(false)
  if (runs.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Recent runs</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            No runs yet. Hit "Run now" above, or wait for the next scheduled tick.
          </p>
        </CardContent>
      </Card>
    )
  }
  const visible = showAll ? runs : runs.slice(0, 10)
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="text-base">Recent runs</CardTitle>
        {runs.length > 10 && (
          <button
            type="button"
            onClick={() => setShowAll((v) => !v)}
            className="text-xs text-muted-foreground hover:text-foreground"
          >
            {showAll ? `Show first 10` : `Show all (${runs.length})`}
          </button>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        {visible.map((r) => (
          <RunCard key={r.id} run={r} />
        ))}
      </CardContent>
    </Card>
  )
}

function CostCard({ runs }: { runs: WorkerRun[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base text-muted-foreground">Cost</CardTitle>
      </CardHeader>
      <CardContent>
        <CostSparkline runs={runs} />
      </CardContent>
    </Card>
  )
}

// ConfigCard — collapsed by default. Diagnostic-only; the user rarely
// wants to see the raw JSON unless something's broken.
function ConfigCard({ worker }: { worker: Worker }) {
  const [open, setOpen] = useState(false)
  return (
    <Card>
      <CardHeader>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex w-full items-center gap-2 text-left"
        >
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          <CardTitle className="text-base text-muted-foreground">Configuration</CardTitle>
          <span className="ml-2 text-[10px] text-muted-foreground/60">click to {open ? 'hide' : 'expand'}</span>
        </button>
      </CardHeader>
      {open && (
        <CardContent>
          <ConfigView worker={worker} />
        </CardContent>
      )}
    </Card>
  )
}

// TemplateUpdateBanner polls the source template's latest version and
// surfaces an "vN available" hint when the registry has moved past the
// version recorded on this Worker.
function TemplateUpdateBanner({ worker }: { worker: Worker }) {
  const [latest, setLatest] = useState<number | null>(null)
  useEffect(() => {
    if (!worker.source_template_name) return
    let cancelled = false
    getWorkerTemplate(worker.source_template_name, 'latest')
      .then((res) => {
        if (!cancelled) setLatest(res.entry.version)
      })
      .catch(() => {
        if (!cancelled) setLatest(null)
      })
    return () => {
      cancelled = true
    }
  }, [worker.source_template_name])
  if (!worker.source_template_name) return null
  if (latest === null) return null
  const installed = worker.source_template_version ?? 0
  if (latest <= installed) return null
  return (
    <div className="rounded-md border border-amber-400/60 bg-amber-100/40 p-3 text-sm dark:bg-amber-950/40">
      <div className="font-medium">Template update available</div>
      <div className="mt-0.5 text-muted-foreground">
        This Worker was installed from{' '}
        <span className="font-mono">
          {worker.source_template_name}@v{installed}
        </span>
        . The registry now has{' '}
        <span className="font-mono font-medium">v{latest}</span>
        . Re-publish the current configuration or install via MCP to pick up
        the changes.
      </div>
    </div>
  )
}

// AutoPausedBanner surfaces the runner's auto-pause reason inline so
// the operator knows WHY the worker stopped.
function AutoPausedBanner({ worker }: { worker: Worker }) {
  if (!worker.auto_paused_reason || worker.auto_paused_reason.trim() === '') {
    return null
  }
  return (
    <div
      role="alert"
      className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm"
    >
      <div className="font-medium text-destructive">Auto-paused</div>
      <div className="mt-0.5 text-destructive/90">{worker.auto_paused_reason}</div>
    </div>
  )
}
