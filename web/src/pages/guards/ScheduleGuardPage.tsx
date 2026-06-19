import { useCallback, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { useApi } from '@/hooks/use-api'
import {
  createScheduledJob,
  deleteScheduledJob,
  getScheduleGuardList,
  runScheduledJob,
} from '@/api/client'
import type { ScheduledJob } from '@/api/client'
import { ArrowLeft, CalendarClock, Loader2, Play, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'

export function ScheduleGuardPage() {
  const fetcher = useCallback(() => getScheduleGuardList(), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  const [busy, setBusy] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)

  async function handleRun(id: string) {
    setBusy(id)
    try {
      await runScheduledJob(id)
      toast.success('Job triggered')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Run failed')
    } finally {
      setBusy(null)
    }
  }

  async function handleDelete(id: string) {
    setBusy(id)
    try {
      await deleteScheduledJob(id)
      toast.success('Job deleted')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="space-y-5 max-w-5xl">
      <Link
        to="/guards"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Guards
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold flex items-center gap-2">
            <CalendarClock className="h-6 w-6" /> Schedule Guard
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Runs cron- and interval-triggered jobs through the same approval
            pipeline as agent calls. Jobs marked &quot;survive daemon down&quot; get
            promoted to launchd / systemd timers.
          </p>
        </div>
        <NewJobDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={refetch} />
      </div>

      {loading && !data ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading schedule…
        </div>
      ) : error ? (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      ) : data ? (
        <Card>
          <CardContent className="p-0">
            {data.jobs.length === 0 ? (
              <div className="p-6 text-center text-sm text-muted-foreground">
                No scheduled jobs yet. Use &quot;New job&quot; above or invoke{' '}
                <span className="font-mono">mcplexer__schedule_create</span> from an agent.
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Kind</TableHead>
                    <TableHead>Spec</TableHead>
                    <TableHead>Next run</TableHead>
                    <TableHead>Last status</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.jobs.map((j) => (
                    <JobRow
                      key={j.id}
                      job={j}
                      busy={busy === j.id}
                      onRun={() => handleRun(j.id)}
                      onDelete={() => handleDelete(j.id)}
                    />
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      ) : null}
    </div>
  )
}

interface JobRowProps {
  job: ScheduledJob
  busy: boolean
  onRun: () => void
  onDelete: () => void
}

function JobRow({ job, busy, onRun, onDelete }: JobRowProps) {
  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{job.name}</div>
        <div className="font-mono text-[10px] text-muted-foreground/70">{job.id}</div>
      </TableCell>
      <TableCell>
        <Badge variant="secondary" className="text-[10px]">{job.kind}</Badge>
      </TableCell>
      <TableCell className="font-mono text-xs">{job.spec}</TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {job.next_run_at ? new Date(job.next_run_at).toLocaleString() : '—'}
      </TableCell>
      <TableCell>
        {job.last_status ? (
          <Badge
            className={
              job.last_status === 'success'
                ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30 text-[10px]'
                : job.last_status === 'failure'
                  ? 'bg-destructive/10 text-destructive border-destructive/30 text-[10px]'
                  : 'text-[10px]'
            }
          >
            {job.last_status}
          </Badge>
        ) : (
          <span className="text-xs text-muted-foreground">never</span>
        )}
      </TableCell>
      <TableCell className="text-right">
        <div className="inline-flex gap-1">
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={onRun}
            data-testid={`schedule-run-${job.id}`}
            title="Run now"
          >
            {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={onDelete}
            data-testid={`schedule-delete-${job.id}`}
            title="Delete"
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      </TableCell>
    </TableRow>
  )
}

interface NewJobDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
}

function NewJobDialog({ open, onOpenChange, onCreated }: NewJobDialogProps) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState('cron')
  const [spec, setSpec] = useState('')
  const [command, setCommand] = useState('')
  const [saving, setSaving] = useState(false)

  async function handleCreate() {
    if (!name.trim() || !kind.trim() || !command.trim()) {
      toast.error('Name, kind, and command are required')
      return
    }
    setSaving(true)
    try {
      await createScheduledJob({ name, kind, spec, command })
      toast.success('Job created')
      setName('')
      setSpec('')
      setCommand('')
      onOpenChange(false)
      onCreated()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Create failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button data-testid="schedule-new">
          <Plus className="mr-1.5 h-4 w-4" /> New job
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New scheduled job</DialogTitle>
          <DialogDescription>
            Define a recurring task. Cron spec uses standard five-field syntax;
            interval uses a Go duration like &quot;5m&quot; or &quot;1h30m&quot;.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 py-2">
          <div className="space-y-1">
            <Label htmlFor="job-name">Name</Label>
            <Input
              id="job-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="nightly backup"
              data-testid="schedule-new-name"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="job-kind">Kind</Label>
            <Input
              id="job-kind"
              value={kind}
              onChange={(e) => setKind(e.target.value)}
              placeholder="cron | interval | file_watch | git_hook"
              data-testid="schedule-new-kind"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="job-spec">Spec</Label>
            <Input
              id="job-spec"
              value={spec}
              onChange={(e) => setSpec(e.target.value)}
              placeholder="0 3 * * *"
              data-testid="schedule-new-spec"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="job-command">Command</Label>
            <Input
              id="job-command"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder="/path/to/script.sh"
              data-testid="schedule-new-command"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={handleCreate} disabled={saving} data-testid="schedule-new-create">
            {saving && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
