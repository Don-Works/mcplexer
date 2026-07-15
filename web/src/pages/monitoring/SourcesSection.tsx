// SourcesSection — one row per watched container/project/service: selector,
// pull cadence, cursor freshness, and the consecutive-failure health
// counter that feeds the source-went-dark rule.
import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { toast } from 'sonner'
import { Plus, Trash2 } from 'lucide-react'
import { createLogSource, deleteLogSource, updateLogSource } from '@/api/monitoring'
import type { LogSource, RemoteHost } from '@/api/monitoring'

interface Props {
  workspaceId: string
  sources: LogSource[]
  hosts: RemoteHost[]
  refetch: () => void
}

type SourceKind = 'docker' | 'compose' | 'swarm' | 'journald'
const emptyDraft = { name: '', remote_host_id: '', selector: '', schedule_spec: '2m', kind: 'docker' as SourceKind }
const KIND_HINT: Record<SourceKind, string> = {
  docker: 'container name',
  compose: 'compose project name',
  swarm: 'swarm service name',
  journald: 'systemd unit (e.g. nginx.service)',
}

function cursorAge(ts?: string): string {
  if (!ts) return 'never pulled'
  const ms = Date.now() - new Date(ts).getTime()
  if (ms < 90_000) return 'fresh'
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m behind`
  return `${Math.round(ms / 3_600_000)}h behind`
}

export function SourcesSection({ workspaceId, sources, hosts, refetch }: Props) {
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState(emptyDraft)
  const [deleteTarget, setDeleteTarget] = useState<LogSource | null>(null)
  const hostName = (id: string) => hosts.find(h => h.id === id)?.name ?? id

  async function submit() {
    try {
      await createLogSource({ ...draft, workspace_id: workspaceId, enabled: true })
      toast.success(`source ${draft.name} added — first pull runs within its cadence`)
      setDraft(emptyDraft)
      setAdding(false)
      refetch()
    } catch (e) {
      toast.error(String(e))
    }
  }

  return (
    <section>
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Log sources
        </h2>
        <Button size="sm" variant="ghost" onClick={() => setAdding(a => !a)} disabled={hosts.length === 0}>
          <Plus className="h-4 w-4" /> source
        </Button>
      </div>

      {sources.length === 0 && !adding && (
        <p className="mt-2 text-sm text-muted-foreground">
          {hosts.length === 0
            ? 'Add a host first, then point sources at its containers.'
            : 'No sources yet. A source is one container, Compose project, Swarm service, or journal unit pulled incrementally and distilled into templates.'}
        </p>
      )}

      {sources.length > 0 && (
        <div className="mt-2 divide-y divide-border border border-border">
          {sources.map(s => (
            <div key={s.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <span className="w-32 truncate font-medium">{s.name}</span>
              <span className="w-28 truncate text-xs text-muted-foreground">{hostName(s.remote_host_id)}</span>
              <span className="flex-1 truncate font-mono text-xs text-muted-foreground">
                {s.kind}:{s.selector} · every {s.schedule_spec}
              </span>
              <span className="text-xs text-muted-foreground">{cursorAge(s.cursor_ts)}</span>
              {s.consecutive_failures > 0 && (
                <Badge tone={s.consecutive_failures >= 5 ? 'critical' : 'warn'}>
                  {s.consecutive_failures} failed pulls
                </Badge>
              )}
              <Badge tone={s.enabled ? 'info' : 'muted'}>{s.enabled ? 'enabled' : 'disabled'}</Badge>
              <Button size="sm" variant="ghost"
                onClick={async () => { await updateLogSource(s.id, { enabled: !s.enabled }); refetch() }}>
                {s.enabled ? 'disable' : 'enable'}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(s)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      {adding && (
        <div className="mt-2 grid grid-cols-2 gap-2 border border-border bg-muted/30 p-3 md:grid-cols-5">
          <Input placeholder="name (e.g. api)" value={draft.name}
            onChange={e => setDraft({ ...draft, name: e.target.value })} />
          <select className="border border-border bg-background px-2 text-sm"
            value={draft.remote_host_id}
            onChange={e => setDraft({ ...draft, remote_host_id: e.target.value })}>
            <option value="">host…</option>
            {hosts.map(h => <option key={h.id} value={h.id}>{h.name}</option>)}
          </select>
          <select className="border border-border bg-background px-2 text-sm"
            value={draft.kind}
            onChange={e => setDraft({ ...draft, kind: e.target.value as SourceKind })}>
            <option value="docker">docker</option>
            <option value="compose">compose</option>
            <option value="swarm">swarm</option>
            <option value="journald">journald</option>
          </select>
          <Input placeholder={KIND_HINT[draft.kind]} value={draft.selector} className="font-mono"
            onChange={e => setDraft({ ...draft, selector: e.target.value })} />
          <Input placeholder="cadence (2m, 30s, cron)" value={draft.schedule_spec} className="font-mono"
            onChange={e => setDraft({ ...draft, schedule_spec: e.target.value })} />
          <div className="flex gap-2">
            <Button size="sm" onClick={submit}
              disabled={!draft.name || !draft.remote_host_id || !draft.selector}>
              add
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setAdding(false)}>cancel</Button>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={o => !o && setDeleteTarget(null)}
        title={`Delete source ${deleteTarget?.name}?`}
        description="Its templates and buffered lines cascade-delete."
        confirmLabel="Delete"
        onConfirm={async () => {
          if (!deleteTarget) return
          await deleteLogSource(deleteTarget.id)
          setDeleteTarget(null)
          refetch()
        }}
      />
    </section>
  )
}
