// WorkerTriggersCard (M4) — surfaces the mesh-trigger CRUD for one
// worker. Lives on the detail page (NOT the editor) because triggers
// are an operational concern; editor stays focused on the worker's
// config.
//
// The card shows existing triggers with a humanised summary + throttle
// hint + edit/delete actions. "+ Add mesh trigger" opens an inline form
// that handles every match dimension. The peer-grants list lives in a
// sibling card (WorkerTriggerPeerGrantsList) so this file stays focused
// on the trigger rows themselves.
//
// All mutations call the dispatcher's reload hook on the backend via
// the admin service — no extra plumbing needed here.

import { useCallback, useEffect, useState } from 'react'
import { Bell, Plus, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { EnableSwitch } from './WorkersListPage'
import {
  createWorkerMeshTrigger,
  deleteWorkerMeshTrigger,
  listWorkerMeshTriggers,
  updateWorkerMeshTrigger,
  type MeshTriggerInput,
  type WorkerMeshTrigger,
} from '@/api/workers'
import { useMeshDiscovery, labelForPeer } from './use-mesh-discovery'

interface WorkerTriggersCardProps {
  workerID: string
  workerName: string
}

export function WorkerTriggersCard({ workerID, workerName }: WorkerTriggersCardProps) {
  const [triggers, setTriggers] = useState<WorkerMeshTrigger[]>([])
  const [adding, setAdding] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const rows = await listWorkerMeshTriggers(workerID)
      setTriggers(rows ?? [])
      setError(null)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [workerID])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const handleCreate = async (input: MeshTriggerInput) => {
    try {
      await createWorkerMeshTrigger(workerID, input)
      toast.success('Mesh trigger created')
      setAdding(false)
      await refresh()
    } catch (e) {
      toast.error('Failed to create trigger: ' + (e as Error).message)
    }
  }

  const handleToggle = async (t: WorkerMeshTrigger) => {
    try {
      await updateWorkerMeshTrigger(workerID, t.id, { enabled: !t.enabled })
      await refresh()
    } catch (e) {
      toast.error('Failed to toggle: ' + (e as Error).message)
    }
  }

  const handleDelete = async (t: WorkerMeshTrigger) => {
    if (!confirm('Delete this mesh trigger? It will stop firing immediately.')) {
      return
    }
    try {
      await deleteWorkerMeshTrigger(workerID, t.id)
      toast.success('Trigger deleted')
      await refresh()
    } catch (e) {
      toast.error('Failed to delete: ' + (e as Error).message)
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <CardTitle className="flex items-center gap-2 text-base">
          <Bell className="h-4 w-4" /> Mesh triggers
          {triggers.length > 0 && (
            <Badge variant="outline" className="font-mono text-[10px]">
              {triggers.length}
            </Badge>
          )}
        </CardTitle>
        {!adding && (
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            <Plus className="mr-1 h-3 w-3" /> Add mesh trigger
          </Button>
        )}
      </CardHeader>
      <CardContent className="space-y-3">
        {loading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : error ? (
          <p className="text-sm text-destructive">{error}</p>
        ) : (
          <>
            {adding && (
              <TriggerForm
                workerName={workerName}
                onCancel={() => setAdding(false)}
                onSubmit={handleCreate}
              />
            )}
            {triggers.length === 0 && !adding ? (
              <p className="text-sm text-muted-foreground">
                No mesh triggers configured. {workerName} only fires on its
                schedule.
              </p>
            ) : (
              triggers.map((t) => (
                <TriggerRow
                  key={t.id}
                  trigger={t}
                  onToggle={() => handleToggle(t)}
                  onDelete={() => handleDelete(t)}
                />
              ))
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}

function TriggerRow({
  trigger,
  onToggle,
  onDelete,
}: {
  trigger: WorkerMeshTrigger
  onToggle: () => void
  onDelete: () => void
}) {
  return (
    <div className="flex items-start justify-between gap-3 rounded border bg-muted/30 p-3">
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm font-medium">{describeMatcher(trigger)}</p>
        <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
          <span>throttle {trigger.throttle_seconds}s</span>
          <span>max chain depth {trigger.max_chain_depth}</span>
          {trigger.from_filters.length > 0 && (
            <span>{trigger.from_filters.length} source filter(s)</span>
          )}
        </div>
      </div>
      <div className="flex items-center gap-2">
        <EnableSwitch enabled={trigger.enabled} busy={false} onToggle={onToggle} />
        <Button size="icon" variant="ghost" onClick={onDelete}>
          <Trash2 className="h-4 w-4 text-muted-foreground" />
        </Button>
      </div>
    </div>
  )
}

function describeMatcher(t: WorkerMeshTrigger): string {
  const parts: string[] = []
  if (t.kind_match) parts.push(`kind=${t.kind_match}`)
  if (t.tag_match) parts.push(`tags=${t.tag_match}`)
  if (t.audience_match && t.audience_match !== '*') {
    parts.push(`audience=${t.audience_match}`)
  }
  if (t.content_regex) parts.push(`content~${t.content_regex}`)
  if (t.from_filters.length > 0) {
    const peers = t.from_filters
      .map((f) => f.peer_id || f.agent_name || f.role)
      .filter(Boolean)
    if (peers.length > 0) parts.push(`from=${peers.join('|')}`)
  }
  if (parts.length === 0) return 'Fires on every mesh message'
  return 'When mesh receives ' + parts.join(' AND ')
}

interface TriggerFormProps {
  workerName: string
  onCancel: () => void
  onSubmit: (input: MeshTriggerInput) => void
}

// FromFilterDraft is the form-local shape for one from-filter row.
// Mapping to the wire format happens at submit time so the form can
// keep an "empty" row in state without it leaking into the payload.
// Role filtering is intentionally absent: the backend rejects it
// ("role filtering is not implemented") because mesh messages don't
// carry sender role on the wire.
interface FromFilterDraft {
  peer_id?: string
  agent_name?: string
}

function TriggerForm({ workerName, onCancel, onSubmit }: TriggerFormProps) {
  const discovery = useMeshDiscovery()
  const [kind, setKind] = useState('')
  const [tagMatch, setTagMatch] = useState('')
  const [audience, setAudience] = useState('')
  const [contentRegex, setContentRegex] = useState('')
  const [fromFilters, setFromFilters] = useState<FromFilterDraft[]>([{}])
  const [throttle, setThrottle] = useState(60)
  const [maxDepth, setMaxDepth] = useState(3)
  const [submitting, setSubmitting] = useState(false)

  const updateFilter = (i: number, patch: Partial<FromFilterDraft>) => {
    setFromFilters((prev) => prev.map((f, idx) => (idx === i ? { ...f, ...patch } : f)))
  }
  const removeFilter = (i: number) => {
    setFromFilters((prev) => (prev.length <= 1 ? [{}] : prev.filter((_, idx) => idx !== i)))
  }
  const addFilter = () => setFromFilters((prev) => [...prev, {}])

  const submit = async () => {
    setSubmitting(true)
    try {
      const input: MeshTriggerInput = {
        throttle_seconds: throttle,
        max_chain_depth: maxDepth,
      }
      if (kind) input.kind_match = kind
      if (tagMatch) input.tag_match = tagMatch
      if (audience && audience !== '*') input.audience_match = audience
      if (contentRegex) input.content_regex = contentRegex
      const liveFilters = fromFilters
        .map((f) => {
          const out: FromFilterDraft = {}
          if (f.peer_id) out.peer_id = f.peer_id
          if (f.agent_name) out.agent_name = f.agent_name
          return out
        })
        .filter((f) => f.peer_id || f.agent_name)
      if (liveFilters.length > 0) input.from_filters = liveFilters
      if (
        !input.kind_match && !input.tag_match && !input.audience_match &&
        !input.content_regex && !input.from_filters
      ) {
        input.all_messages = true
      }
      await onSubmit(input)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="space-y-3 border bg-background p-4">
      <div className="flex items-start justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          Configure when <span className="font-medium">{workerName}</span> fires
          on incoming mesh messages. Suggestions are pulled from live mesh
          activity — type to filter or enter a new value.
        </p>
        <Badge variant="outline" tone="info" className="shrink-0 text-[10px]">
          MCP-configurable
        </Badge>
      </div>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <Field label="Kind">
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value)}
            className="w-full border bg-background px-2 py-1 text-sm"
          >
            <option value="">any kind</option>
            {discovery.kinds.map((k) => (
              <option key={k} value={k}>{k}</option>
            ))}
          </select>
        </Field>
        <Field label="Tags (comma-separated)">
          <Input
            list="mesh-known-tags"
            value={tagMatch}
            onChange={(e) => setTagMatch(e.target.value)}
            placeholder={discovery.tags.length ? 'pick or type…' : 'no tags seen yet'}
          />
          <datalist id="mesh-known-tags">
            {discovery.tags.map((t) => (
              <option key={t} value={t} />
            ))}
          </datalist>
        </Field>
        <Field label="Audience">
          <select
            value={audience}
            onChange={(e) => setAudience(e.target.value)}
            className="w-full border bg-background px-2 py-1 text-sm"
          >
            <option value="">any audience</option>
            {discovery.audiences.map((a) => (
              <option key={a} value={a}>{a === '*' ? '* (broadcast)' : a}</option>
            ))}
          </select>
        </Field>
        <Field label="Throttle (seconds)">
          <Input
            type="number"
            min={1}
            value={throttle}
            onChange={(e) => setThrottle(Number(e.target.value) || 60)}
          />
        </Field>
        <Field label="Max chain depth (1–10)">
          <Input
            type="number"
            min={1}
            max={10}
            value={maxDepth}
            onChange={(e) => setMaxDepth(Number(e.target.value) || 3)}
          />
        </Field>
      </div>

      <div className="space-y-2 border border-border/60 p-3">
        <div className="flex items-center justify-between">
          <Label className="text-xs">From (sender filters)</Label>
          <Button type="button" variant="ghost" size="sm" onClick={addFilter} disabled={submitting}>
            <Plus className="mr-1 h-3 w-3" /> add filter
          </Button>
        </div>
        <p className="text-[11px] text-muted-foreground">
          Any one of the rows below admits the message (OR'd). Within a row, set the peer OR agent — leave the other blank.
        </p>
        {fromFilters.map((f, i) => (
          <div key={i} className="grid grid-cols-1 gap-2 md:grid-cols-[1fr_1fr_auto]">
            <select
              value={f.peer_id ?? ''}
              onChange={(e) => updateFilter(i, { peer_id: e.target.value || undefined })}
              className="w-full border bg-background px-2 py-1 text-sm"
            >
              <option value="">any peer</option>
              {discovery.peers.map((p) => (
                <option key={p.peerID} value={p.peerID}>
                  {labelForPeer(p)}
                </option>
              ))}
            </select>
            <Input
              list="mesh-known-agents"
              value={f.agent_name ?? ''}
              onChange={(e) => updateFilter(i, { agent_name: e.target.value || undefined })}
              placeholder="any agent"
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => removeFilter(i)}
              disabled={submitting}
              aria-label="remove filter"
            >
              <X className="h-3.5 w-3.5 text-muted-foreground" />
            </Button>
          </div>
        ))}
        <datalist id="mesh-known-agents">
          {discovery.agents.map((a) => (
            <option key={a.name} value={a.name} />
          ))}
        </datalist>
      </div>

      <Field label="Content regex (optional)">
        <Input
          value={contentRegex}
          onChange={(e) => setContentRegex(e.target.value)}
          placeholder="e.g. (?i)breach"
        />
      </Field>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={submitting}>
          Cancel
        </Button>
        <Button size="sm" onClick={submit} disabled={submitting}>
          {submitting ? 'Saving…' : 'Create trigger'}
        </Button>
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <Label className="text-xs">{label}</Label>
      {children}
    </div>
  )
}

