// HostsSection — SSH targets the collector reads docker logs from.
// Read-only by construction on the remote side; the interesting state
// here is the TOFU host-key pin and enablement.
import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { toast } from 'sonner'
import { Plus, Trash2, KeyRound } from 'lucide-react'
import {
  createRemoteHost, deleteRemoteHost, repinRemoteHost, updateRemoteHost,
} from '@/api/monitoring'
import type { RemoteHost } from '@/api/monitoring'

interface Props {
  workspaceId: string
  hosts: RemoteHost[]
  authScopes: { id: string; name: string; type: string }[]
  refetch: () => void
}

const emptyDraft = { name: '', ssh_user: 'logwatch', ssh_host: '', ssh_port: 22, auth_scope_id: '' }

export function HostsSection({ workspaceId, hosts, authScopes, refetch }: Props) {
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState(emptyDraft)
  const [deleteTarget, setDeleteTarget] = useState<RemoteHost | null>(null)
  const [repinTarget, setRepinTarget] = useState<RemoteHost | null>(null)
  const sshScopes = authScopes.filter(s => s.type === 'ssh_key' || s.type === 'ssh_agent')

  async function submit() {
    try {
      await createRemoteHost({ ...draft, workspace_id: workspaceId, enabled: true })
      toast.success(`host ${draft.name} added — first dial records its key pin`)
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
          Remote hosts
        </h2>
        <Button size="sm" variant="ghost" onClick={() => setAdding(a => !a)}>
          <Plus className="h-4 w-4" /> host
        </Button>
      </div>

      {hosts.length === 0 && !adding && (
        <p className="mt-2 text-sm text-muted-foreground">
          No hosts yet. A host is an SSH coordinate the runner pulls docker logs
          from; the box needs nothing installed.
        </p>
      )}

      {hosts.length > 0 && (
        <div className="mt-2 divide-y divide-border border border-border">
          {hosts.map(h => (
            <div key={h.id} className="flex items-center gap-3 px-3 py-2 text-sm">
              <span className="w-32 truncate font-medium">{h.name}</span>
              <span className="flex-1 truncate font-mono text-xs text-muted-foreground">
                {h.ssh_user}@{h.ssh_host}:{h.ssh_port}
              </span>
              {h.host_key_pin
                ? <Badge tone="success" title={h.host_key_pin}>pinned</Badge>
                : <Badge tone="warn">unpinned · TOFU on first dial</Badge>}
              <Badge tone={h.enabled ? 'info' : 'muted'}>{h.enabled ? 'enabled' : 'disabled'}</Badge>
              <Button
                size="sm" variant="ghost"
                title={h.enabled ? 'stop pulling from this host' : 'resume pulling'}
                onClick={async () => {
                  await updateRemoteHost(h.id, { enabled: !h.enabled })
                  refetch()
                }}
              >
                {h.enabled ? 'disable' : 'enable'}
              </Button>
              <Button size="sm" variant="ghost" title="clear the host-key pin after a deliberate host rebuild"
                onClick={() => setRepinTarget(h)} disabled={!h.host_key_pin}>
                <KeyRound className="h-4 w-4" />
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(h)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      )}

      {adding && (
        <div className="mt-2 grid grid-cols-2 gap-2 border border-border bg-muted/30 p-3 md:grid-cols-5">
          <Input placeholder="name (e.g. ip-prod-1)" value={draft.name}
            onChange={e => setDraft({ ...draft, name: e.target.value })} />
          <Input placeholder="ssh user" value={draft.ssh_user} className="font-mono"
            onChange={e => setDraft({ ...draft, ssh_user: e.target.value })} />
          <Input placeholder="host / tailscale IP" value={draft.ssh_host} className="font-mono"
            onChange={e => setDraft({ ...draft, ssh_host: e.target.value })} />
          <select
            className="border border-border bg-background px-2 text-sm"
            value={draft.auth_scope_id}
            onChange={e => setDraft({ ...draft, auth_scope_id: e.target.value })}
          >
            <option value="">ssh credential scope…</option>
            {sshScopes.map(s => <option key={s.id} value={s.id}>{s.name} ({s.type})</option>)}
            {sshScopes.length === 0 && authScopes.map(s => (
              <option key={s.id} value={s.id}>{s.name} ({s.type})</option>
            ))}
          </select>
          <div className="flex gap-2">
            <Button size="sm" onClick={submit}
              disabled={!draft.name || !draft.ssh_host || !draft.auth_scope_id}>
              add
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setAdding(false)}>cancel</Button>
          </div>
          {sshScopes.length === 0 && (
            <p className="col-span-full text-xs text-muted-foreground">
              No ssh_key / ssh_agent auth scope yet: create one under Config → Auth scopes
              and store the private key as its <span className="font-mono">private_key</span> secret.
            </p>
          )}
        </div>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={o => !o && setDeleteTarget(null)}
        title={`Delete host ${deleteTarget?.name}?`}
        description="Its log sources, templates, and buffered lines cascade-delete. The remote box itself is untouched."
        confirmLabel="Delete"
        onConfirm={async () => {
          if (!deleteTarget) return
          await deleteRemoteHost(deleteTarget.id)
          setDeleteTarget(null)
          refetch()
        }}
      />
      <ConfirmDialog
        open={repinTarget !== null}
        onOpenChange={o => !o && setRepinTarget(null)}
        title={`Re-pin host key for ${repinTarget?.name}?`}
        description="Only do this after a deliberate host rebuild. The current pin is cleared and the next successful dial records the new key. A pin MISMATCH is never resolved automatically."
        confirmLabel="Clear pin"
        onConfirm={async () => {
          if (!repinTarget) return
          await repinRemoteHost(repinTarget.id)
          toast.success('pin cleared — next dial re-records it')
          setRepinTarget(null)
          refetch()
        }}
      />
    </section>
  )
}
