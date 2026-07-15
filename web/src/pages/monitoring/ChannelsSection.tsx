// ChannelsSection — the alert-output levers. Each channel row carries
// its own min_severity floor: an incident fans out to every enabled
// channel whose floor admits it. Credentials never appear here —
// webhook URLs and chat ids live in the secrets store as secret://
// refs, and the daemon resolves them only at send time.
import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { toast } from 'sonner'
import { Plus, Trash2, Send } from 'lucide-react'
import {
  createChannel, deleteChannel, sendTestNotification, updateChannel, SEVERITIES,
} from '@/api/monitoring'
import type { ChannelKind, MonitoringChannel, RemoteHost, Severity } from '@/api/monitoring'

interface Props {
  workspaceId: string
  channels: MonitoringChannel[]
  hosts: RemoteHost[]
  notifyEnabled: boolean
  refetch: () => void
}

interface Draft {
  name: string
  kind: ChannelKind
  min_severity: Severity
  webhook_ref: string
  auth_scope_id: string
  chat_id: string
  chat_id_ref: string
  session_id: string
}

const emptyDraft: Draft = {
  name: '', kind: 'gchat_webhook', min_severity: 'error',
  webhook_ref: '', auth_scope_id: '', chat_id: '', chat_id_ref: '', session_id: '',
}

function draftConfig(d: Draft): string {
  switch (d.kind) {
    case 'gchat_webhook':
      return JSON.stringify({ auth_scope_id: d.auth_scope_id, webhook_ref: d.webhook_ref })
    case 'telegram':
      return JSON.stringify({ chat_id: d.chat_id })
    case 'whatsapp':
      return JSON.stringify({ chat_id_ref: d.chat_id_ref, session_id: d.session_id })
    default:
      return '{}'
  }
}

export function ChannelsSection({ workspaceId, channels, hosts, notifyEnabled, refetch }: Props) {
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [deleteTarget, setDeleteTarget] = useState<MonitoringChannel | null>(null)
  const [testing, setTesting] = useState(false)

  async function submit() {
    try {
      await createChannel({
        workspace_id: workspaceId, name: draft.name, kind: draft.kind,
        min_severity: draft.min_severity, config_json: draftConfig(draft), enabled: true,
      })
      toast.success(`channel ${draft.name} added`)
      setDraft(emptyDraft)
      setAdding(false)
      refetch()
    } catch (e) {
      toast.error(String(e))
    }
  }

  async function runTest(severity: Severity) {
    setTesting(true)
    try {
      await sendTestNotification(workspaceId, severity, hosts[0]?.id)
      toast.success(`test ${severity} dispatched — every enabled channel with floor ≤ ${severity} should receive it`)
    } catch (e) {
      toast.error(String(e))
    } finally {
      setTesting(false)
    }
  }

  return (
    <section>
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Alert channels
        </h2>
        <div className="flex items-center gap-2">
          {notifyEnabled && channels.length > 0 && (
            <Button size="sm" variant="ghost" disabled={testing} onClick={() => runTest('critical')}>
              <Send className="h-4 w-4" /> test critical
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => setAdding(a => !a)}>
            <Plus className="h-4 w-4" /> channel
          </Button>
        </div>
      </div>

      {channels.length === 0 && !adding && (
        <p className="mt-2 text-sm text-muted-foreground">
          No configured channels. New critical incidents still enter Signal and
          attempt Web Push when a device is subscribed. Add a team route such as
          Google Chat and an independent critical-only route such as WhatsApp.
        </p>
      )}

      {channels.length > 0 && (
        <div className="mt-2 divide-y divide-border border border-border">
          {channels.map(c => (
            <div key={c.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <span className="w-32 truncate font-medium">{c.name}</span>
              <Badge tone="mono">{c.kind}</Badge>
              <div className="flex flex-1 items-center gap-1">
                <span className="text-xs text-muted-foreground">fires at</span>
                {SEVERITIES.map(sev => (
                  <button
                    key={sev}
                    className={
                      'border px-1.5 py-0.5 text-xs ' +
                      (c.min_severity === sev
                        ? 'border-primary/60 bg-accent text-foreground'
                        : 'border-border text-muted-foreground hover:bg-muted')
                    }
                    title={`fan out when incident severity ≥ ${sev}`}
                    onClick={async () => {
                      await updateChannel(c.id, { min_severity: sev })
                      refetch()
                    }}
                  >
                    {sev}+
                  </button>
                ))}
              </div>
              <Badge tone={c.enabled ? 'info' : 'muted'}>{c.enabled ? 'enabled' : 'disabled'}</Badge>
              <Button size="sm" variant="ghost"
                onClick={async () => { await updateChannel(c.id, { enabled: !c.enabled }); refetch() }}>
                {c.enabled ? 'disable' : 'enable'}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(c)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      {adding && (
        <div className="mt-2 space-y-2 border border-border bg-muted/30 p-3">
          <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
            <Input placeholder="name (e.g. incidents)" value={draft.name}
              onChange={e => setDraft({ ...draft, name: e.target.value })} />
            <select className="border border-border bg-background px-2 text-sm" value={draft.kind}
              onChange={e => setDraft({ ...draft, kind: e.target.value as ChannelKind })}>
              <option value="gchat_webhook">gchat_webhook</option>
              <option value="telegram">telegram</option>
              <option value="whatsapp">whatsapp</option>
              <option value="mesh">mesh</option>
            </select>
            <select className="border border-border bg-background px-2 text-sm" value={draft.min_severity}
              onChange={e => setDraft({ ...draft, min_severity: e.target.value as Severity })}>
              {SEVERITIES.map(s => <option key={s} value={s}>fires at {s}+</option>)}
            </select>
            <div className="flex gap-2">
              <Button size="sm" onClick={submit} disabled={!draft.name}>add</Button>
              <Button size="sm" variant="ghost" onClick={() => setAdding(false)}>cancel</Button>
            </div>
          </div>
          {draft.kind === 'gchat_webhook' && (
            <div className="grid grid-cols-2 gap-2">
              <Input placeholder="secret://GCHAT_WEBHOOK_INCIDENTS" className="font-mono"
                value={draft.webhook_ref}
                onChange={e => setDraft({ ...draft, webhook_ref: e.target.value })} />
              <Input placeholder="auth scope id holding that secret" className="font-mono"
                value={draft.auth_scope_id}
                onChange={e => setDraft({ ...draft, auth_scope_id: e.target.value })} />
              <p className="col-span-full text-xs text-muted-foreground">
                Paste the webhook URL into the secrets store first (Config → Auth scopes →
                secrets); only its secret:// ref is stored here. Plaintext URLs are rejected.
              </p>
            </div>
          )}
          {draft.kind === 'telegram' && (
            <Input placeholder="workspace-bound telegram chat id" className="font-mono"
              value={draft.chat_id}
              onChange={e => setDraft({ ...draft, chat_id: e.target.value })} />
          )}
          {draft.kind === 'whatsapp' && (
            <div className="grid grid-cols-2 gap-2">
              <Input placeholder="secret://WHATSAPP_PERSONAL_CHAT_ID" className="font-mono"
                value={draft.chat_id_ref}
                onChange={e => setDraft({ ...draft, chat_id_ref: e.target.value })} />
              <Input placeholder="openwa session id" className="font-mono"
                value={draft.session_id}
                onChange={e => setDraft({ ...draft, session_id: e.target.value })} />
              <p className="col-span-full text-xs text-muted-foreground">
                Reserved for critical in practice: set the floor to critical+. The chat id
                (your number) stays a secret ref end to end.
              </p>
            </div>
          )}
        </div>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={o => !o && setDeleteTarget(null)}
        title={`Delete channel ${deleteTarget?.name}?`}
        description="Incidents stop fanning out here immediately. The underlying secret is untouched."
        confirmLabel="Delete"
        onConfirm={async () => {
          if (!deleteTarget) return
          await deleteChannel(deleteTarget.id)
          setDeleteTarget(null)
          refetch()
        }}
      />
    </section>
  )
}
