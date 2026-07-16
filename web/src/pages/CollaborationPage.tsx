import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  AlertTriangle,
  Check,
  ChevronRight,
  KeyRound,
  Laptop,
  Loader2,
  LockKeyhole,
  Plus,
  Server,
  Settings2,
  ShieldCheck,
  RefreshCw,
  UserRound,
} from 'lucide-react'
import { toast } from 'sonner'

import {
  createCollaborationInvitation,
  enrollLocalIdentity,
  getCollaboration,
  joinCollaborationInvitation,
  revokeDevice,
  revokeIdentityKey,
  revokePrincipal,
  setWorkspaceGrants,
  syncWorkspaceMembership,
  updateWorkspacePolicy,
  type CollaborationPrincipal,
  type CollaborationSnapshot,
  type CollaborationWorkspace,
  type InvitationResult,
  type PrincipalIdentityKey,
  type PrincipalKind,
  type WorkspacePublicationPolicy,
} from '@/api/collaboration'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { useConfirm } from '@/components/ui/confirm-dialog'
import { CopyButton } from '@/components/ui/copy-button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { useApi } from '@/hooks/use-api'
import { cn } from '@/lib/utils'

const capabilityCopy: Record<string, { label: string; help: string }> = {
  'workspace.view': { label: 'See workspace', help: 'Discover the workspace and its shared task index.' },
  'tasks.read': { label: 'Read shared tasks', help: 'Read only tasks whose visibility also includes this principal.' },
  'tasks.create': { label: 'Create tasks', help: 'Create a local draft and publish it to the workspace home.' },
  'tasks.publish': { label: 'Publish sanitized tasks', help: 'Machine-safe write-only path for monitors and collectors.' },
  'tasks.edit': { label: 'Edit tasks', help: 'Publish content and status edits against the last synced home revision.' },
}

const profileCopy: Record<string, string> = {
  none: 'No access',
  reader: 'Reader',
  contributor: 'Contributor',
  editor: 'Editor',
  machine_publisher: 'Machine publisher',
  custom: 'Custom',
}

function sameSet(left: string[], right: string[]): boolean {
  if (left.length !== right.length) return false
  const expected = new Set(right)
  return left.every((value) => expected.has(value))
}

function grantsFor(snapshot: CollaborationSnapshot, principalId: string, shareId: string): string[] {
  const available = new Set(snapshot.capabilities)
  const granted = snapshot.workspaces
    .find((workspace) => workspace.share_id === shareId)
    ?.grants.filter((grant) => grant.principal_id === principalId && !grant.revoked_at)
    .map((grant) => grant.capability) ?? []
  return granted.filter((capability) => available.has(capability))
}

function profileFor(snapshot: CollaborationSnapshot, capabilities: string[]): string {
  if (capabilities.length === 0) return 'none'
  for (const [profile, values] of Object.entries(snapshot.profiles)) {
    if (sameSet(capabilities, values)) return profile
  }
  return 'custom'
}

function workspaceLabel(workspace: CollaborationWorkspace): string {
  return workspace.workspace?.name || workspace.local_workspace_id
}

export function CollaborationPage() {
  const fetcher = useCallback((signal: AbortSignal) => getCollaboration(signal), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  const [inviteKind, setInviteKind] = useState<PrincipalKind | null>(null)
  const [joinOpen, setJoinOpen] = useState(false)
  const [enrollOpen, setEnrollOpen] = useState(false)
  const [grantCell, setGrantCell] = useState<{ principal: CollaborationPrincipal; workspace: CollaborationWorkspace } | null>(null)
  const [policyWorkspace, setPolicyWorkspace] = useState<CollaborationWorkspace | null>(null)
  const [inviteResult, setInviteResult] = useState<InvitationResult | null>(null)
  const [syncingShare, setSyncingShare] = useState<string | null>(null)

  const owner = data?.principals.find((principal) => principal.is_local_owner)
  const ownerEnrolled = owner?.devices.some((device) => device.status === 'active') ?? false
  const visiblePrincipals = data?.principals.filter((principal) => !principal.is_local_owner) ?? []

  const syncMembership = async (shareId: string) => {
    setSyncingShare(shareId)
    try {
      await syncWorkspaceMembership(shareId)
      toast.success('Workspace mirror is up to date')
      await refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not sync workspace')
    } finally {
      setSyncingShare(null)
    }
  }

  if (loading && !data) {
    return <PageState icon={<Loader2 className="h-5 w-5 animate-spin" />} title="Loading collaboration access…" />
  }
  if (error || !data) {
    return <PageState icon={<AlertTriangle className="h-5 w-5" />} title="Collaboration access is unavailable" detail={error ?? 'No response'} />
  }

  return (
    <div className="space-y-7" data-testid="collaboration-page">
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-border pb-5">
        <div className="max-w-2xl space-y-1.5">
          <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.16em] text-primary">
            <ShieldCheck className="h-4 w-4" />
            Proof-bound sharing
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">People, machines and workspace access</h1>
          <p className="text-sm leading-6 text-muted-foreground">
            SSH keys prove identity. Devices are bound separately. Workspace grants and each task’s visibility both have to allow a read.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => setJoinOpen(true)}>
            <KeyRound className="h-4 w-4" />
            Join with invite
          </Button>
          <Button variant="outline" onClick={() => setInviteKind('machine')} disabled={!data.enabled || !ownerEnrolled}>
            <Server className="h-4 w-4" />
            Add machine
          </Button>
          <Button onClick={() => setInviteKind('person')} disabled={!data.enabled || !ownerEnrolled}>
            <UserRound className="h-4 w-4" />
            Add person
          </Button>
        </div>
      </header>

      {!data.enabled ? (
        <Notice tone="warn" title="P2P is not running on this node">
          Start a P2P-enabled daemon before creating or joining invitations. Existing policy remains visible and default-deny.
        </Notice>
      ) : !ownerEnrolled ? (
        <Notice tone="info" title="Enroll this device before inviting others">
          <span>Load your Ed25519 identity key into ssh-agent, then prove possession. MCPlexer stores only the public key and proof receipt.</span>
          <Button size="sm" onClick={() => setEnrollOpen(true)}>
            Enroll identity
            <ChevronRight className="h-3.5 w-3.5" />
          </Button>
        </Notice>
      ) : null}

      {data.memberships.length > 0 ? (
        <section className="space-y-3" aria-labelledby="joined-workspaces-title">
          <div>
            <h2 id="joined-workspaces-title" className="text-base font-semibold">Workspaces shared with this device</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              These are local mirrors. The named home device remains authoritative and checks every read or write against its current grants.
            </p>
          </div>
          <div className="grid gap-px border border-border bg-border sm:grid-cols-2 xl:grid-cols-3">
            {data.memberships.map((membership) => (
              <div key={membership.share_id} className="space-y-3 bg-background p-4">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{membership.workspace_name}</div>
                    <div className="mt-1 truncate font-mono text-[10px] text-muted-foreground" title={membership.home_peer_id}>
                      home {membership.home_peer_id}
                    </div>
                  </div>
                  <Badge variant={membership.status === 'active' ? 'default' : 'secondary'}>{membership.status}</Badge>
                </div>
                <div className="flex flex-wrap gap-1">
                  {membership.capabilities.map((capability) => (
                    <Badge key={capability} variant="outline" className="font-mono text-[9px]">{capability}</Badge>
                  ))}
                </div>
                <div className="flex items-center justify-between gap-2 border-t border-border pt-2 text-[10px] text-muted-foreground">
                  <span>Local mirror: {membership.local_workspace_id}</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 px-2 text-[10px]"
                    disabled={membership.status !== 'active' || syncingShare !== null}
                    onClick={() => syncMembership(membership.share_id)}
                    title={membership.cursor_hlc || 'Waiting for first revision'}
                  >
                    <RefreshCw className={cn('h-3 w-3', syncingShare === membership.share_id && 'animate-spin')} />
                    Sync · epoch {membership.access_epoch}
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </section>
      ) : null}

      <section className="space-y-3" aria-labelledby="matrix-title">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 id="matrix-title" className="text-base font-semibold">Permissions matrix</h2>
            <p className="mt-1 text-xs text-muted-foreground">
              Click a cell for exact capabilities. Changing a cell advances that workspace’s access epoch immediately.
            </p>
          </div>
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
            <LockKeyhole className="h-3.5 w-3.5" />
            No legacy peer scope grants access
          </div>
        </div>

        <div className="border border-border bg-card/30">
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="sticky left-0 z-10 min-w-56 bg-background/95 px-4">Principal</TableHead>
                {data.workspaces.map((workspace) => (
                  <TableHead key={workspace.share_id} className="min-w-44 px-3 py-2 align-bottom">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="truncate text-xs font-medium">{workspaceLabel(workspace)}</div>
                        <div className="mt-0.5 font-mono text-[9px] text-muted-foreground">epoch {workspace.access_epoch}</div>
                      </div>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`Sharing policy for ${workspaceLabel(workspace)}`}
                        onClick={() => setPolicyWorkspace(workspace)}
                      >
                        <Settings2 className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {visiblePrincipals.map((principal) => (
                <TableRow key={principal.id}>
                  <TableCell className="sticky left-0 z-10 bg-background/95 px-4 py-3">
                    <PrincipalLabel principal={principal} />
                  </TableCell>
                  {data.workspaces.map((workspace) => {
                    const capabilities = grantsFor(data, principal.id, workspace.share_id)
                    const profile = profileFor(data, capabilities)
                    return (
                      <TableCell key={workspace.share_id} className="p-2">
                        <button
                          type="button"
                          disabled={principal.status !== 'active'}
                          onClick={() => setGrantCell({ principal, workspace })}
                          className={cn(
                            'group w-full border px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                            capabilities.length > 0
                              ? 'border-primary/30 bg-primary/5 hover:bg-primary/10'
                              : 'border-border bg-background/30 hover:bg-muted/50',
                            principal.status !== 'active' && 'cursor-not-allowed opacity-50',
                          )}
                        >
                          <div className="flex items-center justify-between gap-2">
                            <span className="text-xs font-medium">{profileCopy[profile] ?? profile}</span>
                            <ChevronRight className="h-3.5 w-3.5 text-muted-foreground opacity-50 group-hover:opacity-100" />
                          </div>
                          <div className="mt-1 text-[10px] text-muted-foreground">
                            {capabilities.length === 0 ? 'Default deny' : `${capabilities.length} exact permission${capabilities.length === 1 ? '' : 's'}`}
                          </div>
                        </button>
                      </TableCell>
                    )
                  })}
                </TableRow>
              ))}
              {visiblePrincipals.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={Math.max(1, data.workspaces.length + 1)} className="h-28 text-center text-sm text-muted-foreground">
                    No collaborators yet. Add a person or a machine; access starts empty.
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </div>
      </section>

      <section className="space-y-3" aria-labelledby="identities-title">
        <div>
          <h2 id="identities-title" className="text-base font-semibold">Identity and devices</h2>
          <p className="mt-1 text-xs text-muted-foreground">Revoke a device without deleting its person. Revoke a principal to remove every key, device and grant.</p>
        </div>
        <div className="divide-y divide-border border border-border">
          {data.principals.map((principal) => (
            <IdentityRow
              key={principal.id}
              principal={principal}
              onChanged={refetch}
              onInviteCreated={setInviteResult}
            />
          ))}
        </div>
      </section>

      <InvitePrincipalDialog
        open={inviteKind !== null}
        kind={inviteKind ?? 'person'}
        snapshot={data}
        onOpenChange={(open) => { if (!open) setInviteKind(null) }}
        onCreated={(result) => {
          setInviteKind(null)
          setInviteResult(result)
          refetch()
        }}
      />
      <InviteCodeDialog result={inviteResult} onOpenChange={(open) => { if (!open) setInviteResult(null) }} />
      <JoinDialog open={joinOpen} onOpenChange={setJoinOpen} onJoined={refetch} />
      <EnrollDialog open={enrollOpen} onOpenChange={setEnrollOpen} onEnrolled={refetch} />
      <GrantDialog
        cell={grantCell}
        snapshot={data}
        onOpenChange={(open) => { if (!open) setGrantCell(null) }}
        onSaved={refetch}
      />
      <PolicyDialog
        workspace={policyWorkspace}
        onOpenChange={(open) => { if (!open) setPolicyWorkspace(null) }}
        onSaved={refetch}
      />
    </div>
  )
}

function PageState({ icon, title, detail }: { icon: React.ReactNode; title: string; detail?: string }) {
  return (
    <div className="grid min-h-64 place-items-center border border-border bg-card/30 p-8 text-center">
      <div className="space-y-2 text-muted-foreground">
        <div className="mx-auto w-fit">{icon}</div>
        <div className="text-sm text-foreground">{title}</div>
        {detail ? <div className="max-w-lg text-xs">{detail}</div> : null}
      </div>
    </div>
  )
}

function Notice({ tone, title, children }: { tone: 'warn' | 'info'; title: string; children: React.ReactNode }) {
  return (
    <div className={cn(
      'flex flex-wrap items-center justify-between gap-3 border px-4 py-3 text-sm',
      tone === 'warn' ? 'border-amber-500/40 bg-amber-500/10' : 'border-sky-500/40 bg-sky-500/10',
    )}>
      <div className="flex min-w-0 items-start gap-3">
        {tone === 'warn' ? <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-300" /> : <KeyRound className="mt-0.5 h-4 w-4 shrink-0 text-sky-300" />}
        <div>
          <div className="font-medium">{title}</div>
          <div className="mt-0.5 text-xs leading-5 text-muted-foreground">{children}</div>
        </div>
      </div>
    </div>
  )
}

function PrincipalLabel({ principal }: { principal: CollaborationPrincipal }) {
  const Icon = principal.kind === 'machine' ? Server : UserRound
  return (
    <div className="flex min-w-0 items-center gap-3">
      <div className="grid h-8 w-8 shrink-0 place-items-center border border-border bg-muted/30">
        <Icon className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <span className="truncate text-xs font-medium">{principal.display_name}</span>
          {principal.is_local_owner ? <Badge variant="outline" tone="info" className="text-[9px]">owner</Badge> : null}
        </div>
        <div className="mt-0.5 flex items-center gap-2 text-[10px] text-muted-foreground">
          <span>{principal.kind}</span>
          <span>·</span>
          <span className={principal.status === 'active' ? 'text-emerald-400' : ''}>{principal.status.replace('_', ' ')}</span>
        </div>
      </div>
    </div>
  )
}

function InvitePrincipalDialog({
  open,
  kind,
  snapshot,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  kind: PrincipalKind
  snapshot: CollaborationSnapshot
  onOpenChange: (open: boolean) => void
  onCreated: (result: InvitationResult) => void
}) {
  const [name, setName] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [workspaceProfiles, setWorkspaceProfiles] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (!name.trim() || !publicKey.trim()) return
    setBusy(true)
    try {
      const workspaceGrants = Object.entries(workspaceProfiles)
        .filter(([, profile]) => profile !== 'none')
        .map(([shareId, profile]) => ({
          share_id: shareId,
          capabilities: snapshot.profiles[profile] ?? [],
        }))
      const result = await createCollaborationInvitation({
        purpose: 'new_principal', kind, display_name: name.trim(), public_key: publicKey.trim(),
        workspace_grants: workspaceGrants, expires_in_hours: 168,
      })
      setName('')
      setPublicKey('')
      setWorkspaceProfiles({})
      onCreated(result)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not create invitation')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add {kind === 'machine' ? 'a machine' : 'a person'}</DialogTitle>
          <DialogDescription>
            Their Ed25519 SSH key proves possession when they join. Select only the workspaces they need.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="collaboration-name">Display name</Label>
              <Input id="collaboration-name" value={name} onChange={(event) => setName(event.target.value)} placeholder={kind === 'machine' ? 'production log monitor' : 'team member'} />
            </div>
            <div className="space-y-2">
              <Label>Identity type</Label>
              <div className="flex h-9 items-center gap-2 border border-border bg-muted/20 px-3 text-sm">
                {kind === 'machine' ? <Server className="h-4 w-4" /> : <UserRound className="h-4 w-4" />}
                {kind === 'machine' ? 'Machine principal' : 'Person principal'}
              </div>
            </div>
          </div>
          <div className="space-y-2">
            <Label htmlFor="collaboration-public-key">OpenSSH public key</Label>
            <Textarea id="collaboration-public-key" value={publicKey} onChange={(event) => setPublicKey(event.target.value)} rows={3} className="font-mono text-xs" placeholder="ssh-ed25519 AAAAC3…" />
            <p className="text-[11px] leading-5 text-muted-foreground">Only ssh-ed25519 is accepted. Private keys never enter MCPlexer; the joining daemon signs through ssh-agent.</p>
          </div>
          <div className="space-y-2">
            <Label>Initial workspace access</Label>
            <div className="divide-y divide-border border border-border">
              {snapshot.workspaces.map((workspace) => {
                const selected = workspaceProfiles[workspace.share_id] ?? 'none'
                return (
                  <div key={workspace.share_id} className="flex flex-wrap items-center justify-between gap-3 px-3 py-2.5">
                    <div>
                      <div className="text-xs font-medium">{workspaceLabel(workspace)}</div>
                      <div className="text-[10px] text-muted-foreground">Starts with no access until you choose a profile.</div>
                    </div>
                    <Select value={selected} onValueChange={(value) => setWorkspaceProfiles((current) => ({ ...current, [workspace.share_id]: value }))}>
                      <SelectTrigger size="sm" className="w-44"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="none">No access</SelectItem>
                        {Object.keys(snapshot.profiles)
                          .filter((profile) => kind === 'machine' || profile !== 'machine_publisher')
                          .map((profile) => <SelectItem key={profile} value={profile}>{profileCopy[profile] ?? profile}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  </div>
                )
              })}
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={submit} disabled={busy || !name.trim() || !publicKey.trim()}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
            Create invitation
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function InviteCodeDialog({ result, onOpenChange }: { result: InvitationResult | null; onOpenChange: (open: boolean) => void }) {
  return (
    <Dialog open={result !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Invitation ready</DialogTitle>
          <DialogDescription>
            Send this once. It expires at {result ? new Date(result.invitation.expires_at).toLocaleString() : ''} and cannot be recovered from the database.
          </DialogDescription>
        </DialogHeader>
        {result ? (
          <div className="space-y-4">
            <div className="border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-100">
              The code is a bearer secret until the SSH proof succeeds. Use a private channel.
            </div>
            <div className="flex items-start gap-2 border border-border bg-muted/20 p-3">
              <code className="max-h-36 min-w-0 flex-1 overflow-auto break-all font-mono text-[11px] leading-5">{result.invite_code}</code>
              <CopyButton value={result.invite_code} className="mt-0.5" />
            </div>
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <KeyRound className="h-3.5 w-3.5" />
              Pinned to {result.identity_key.fingerprint}
            </div>
          </div>
        ) : null}
        <DialogFooter><Button onClick={() => onOpenChange(false)}>Done</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function JoinDialog({ open, onOpenChange, onJoined }: { open: boolean; onOpenChange: (open: boolean) => void; onJoined: () => void }) {
  const [invitation, setInvitation] = useState('')
  const [deviceName, setDeviceName] = useState('')
  const [deviceKind, setDeviceKind] = useState<'laptop' | 'server' | 'daemon' | 'unknown'>('laptop')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    try {
      await joinCollaborationInvitation({ invitation: invitation.trim(), device_name: deviceName.trim(), device_kind: deviceKind })
      toast.success('Identity verified and workspace access activated')
      setInvitation('')
      setDeviceName('')
      onOpenChange(false)
      onJoined()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not join invitation')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Join a collaboration</DialogTitle>
          <DialogDescription>The matching SSH key must already be loaded in this daemon’s ssh-agent.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="join-code">Invitation code</Label>
            <Textarea id="join-code" value={invitation} onChange={(event) => setInvitation(event.target.value)} rows={4} className="font-mono text-xs" />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2"><Label htmlFor="join-device">Device name</Label><Input id="join-device" value={deviceName} onChange={(event) => setDeviceName(event.target.value)} placeholder="work laptop" /></div>
            <div className="space-y-2">
              <Label>Device kind</Label>
              <Select value={deviceKind} onValueChange={(value) => setDeviceKind(value as typeof deviceKind)}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>{['laptop', 'server', 'daemon', 'unknown'].map((kind) => <SelectItem key={kind} value={kind}>{kind}</SelectItem>)}</SelectContent>
              </Select>
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={submit} disabled={busy || !invitation.trim() || !deviceName.trim()}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}
            Verify and join
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function EnrollDialog({ open, onOpenChange, onEnrolled }: { open: boolean; onOpenChange: (open: boolean) => void; onEnrolled: () => void }) {
  const [publicKey, setPublicKey] = useState('')
  const [deviceName, setDeviceName] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    setBusy(true)
    try {
      await enrollLocalIdentity({ public_key: publicKey.trim(), device_name: deviceName.trim(), device_kind: 'laptop' })
      toast.success('Local SSH identity enrolled')
      setPublicKey('')
      setDeviceName('')
      onOpenChange(false)
      onEnrolled()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not enroll identity')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>Enroll this device</DialogTitle><DialogDescription>MCPlexer challenges ssh-agent and stores the public proof receipt.</DialogDescription></DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2"><Label htmlFor="enroll-name">Device name</Label><Input id="enroll-name" value={deviceName} onChange={(event) => setDeviceName(event.target.value)} placeholder="personal laptop" /></div>
          <div className="space-y-2"><Label htmlFor="enroll-key">Your OpenSSH public key</Label><Textarea id="enroll-key" value={publicKey} onChange={(event) => setPublicKey(event.target.value)} rows={3} className="font-mono text-xs" placeholder="ssh-ed25519 AAAAC3…" /></div>
        </div>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button><Button onClick={submit} disabled={busy || !publicKey.trim() || !deviceName.trim()}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}Enroll</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function GrantDialog({ cell, snapshot, onOpenChange, onSaved }: { cell: { principal: CollaborationPrincipal; workspace: CollaborationWorkspace } | null; snapshot: CollaborationSnapshot; onOpenChange: (open: boolean) => void; onSaved: () => void }) {
  const initial = useMemo(() => cell ? grantsFor(snapshot, cell.principal.id, cell.workspace.share_id) : [], [cell, snapshot])
  const [capabilities, setCapabilities] = useState<string[]>(initial)
  const [busy, setBusy] = useState(false)
  const { confirm, dialog: confirmDialog } = useConfirm()
  const resetKey = cell ? `${cell.principal.id}:${cell.workspace.share_id}:${initial.join(',')}` : ''
  useEffect(() => { setCapabilities(initial) }, [initial, resetKey])
  const dirty = !sameSet(capabilities, initial)

  const selectProfile = (profile: string) => setCapabilities(profile === 'none' ? [] : [...(snapshot.profiles[profile] ?? capabilities)])
  const save = async () => {
    if (!cell || !dirty) return
    const removed = initial.filter((capability) => !capabilities.includes(capability))
    if (removed.length > 0) {
      const confirmed = await confirm({
        title: capabilities.length === 0 ? 'Remove all workspace access?' : 'Reduce workspace access?',
        description: `This advances the workspace access epoch and stops the removed capabilities for ${cell.principal.display_name}. Their devices learn the new access on authenticated sync; copies already received cannot be erased remotely.`,
        confirmLabel: capabilities.length === 0 ? 'Remove access' : 'Reduce access',
        variant: 'destructive',
      })
      if (!confirmed) return
    }
    setBusy(true)
    try {
      await setWorkspaceGrants(cell.workspace.share_id, cell.principal.id, capabilities)
      toast.success(`Access updated for ${cell.principal.display_name}`)
      onOpenChange(false)
      onSaved()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not update access')
    } finally { setBusy(false) }
  }

  return (
    <Dialog open={cell !== null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader><DialogTitle>{cell?.principal.display_name} · {cell ? workspaceLabel(cell.workspace) : ''}</DialogTitle><DialogDescription>Profiles are shortcuts. The checkboxes below are the authorization truth.</DialogDescription></DialogHeader>
        <div className="space-y-4">
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant={capabilities.length === 0 ? 'default' : 'outline'} onClick={() => selectProfile('none')}>No access</Button>
            {Object.keys(snapshot.profiles).map((profile) => <Button key={profile} size="sm" variant={sameSet(capabilities, snapshot.profiles[profile]) ? 'default' : 'outline'} onClick={() => selectProfile(profile)}>{profileCopy[profile] ?? profile}</Button>)}
          </div>
          <div className="grid gap-px border border-border bg-border sm:grid-cols-2">
            {snapshot.capabilities.map((capability) => {
              const checked = capabilities.includes(capability)
              const copy = capabilityCopy[capability] ?? { label: capability, help: capability }
              return (
                <label key={capability} className="flex cursor-pointer items-start gap-3 bg-background p-3 hover:bg-muted/30">
                  <Checkbox checked={checked} onCheckedChange={(value) => setCapabilities((current) => value === true ? [...current, capability] : current.filter((item) => item !== capability))} />
                  <span><span className="block text-xs font-medium">{copy.label}</span><span className="mt-0.5 block text-[10px] leading-4 text-muted-foreground">{copy.help}</span></span>
                </label>
              )
            })}
          </div>
        </div>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button><Button onClick={save} disabled={busy || !dirty}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}Save access</Button></DialogFooter>
      </DialogContent>
      {confirmDialog}
    </Dialog>
  )
}

function PolicyDialog({ workspace, onOpenChange, onSaved }: { workspace: CollaborationWorkspace | null; onOpenChange: (open: boolean) => void; onSaved: () => void }) {
  const [policy, setPolicy] = useState<WorkspacePublicationPolicy | null>(workspace?.policy ?? null)
  const [busy, setBusy] = useState(false)
  const resetKey = workspace ? `${workspace.share_id}:${JSON.stringify(workspace.policy)}` : ''
  useEffect(() => { setPolicy(workspace?.policy ?? null) }, [resetKey, workspace?.policy])
  const dirty = policy !== null && workspace?.policy !== undefined && (
    policy.default_visibility !== workspace.policy.default_visibility ||
    policy.agent_visibility_ceiling !== workspace.policy.agent_visibility_ceiling ||
    policy.widening_requires_approval !== workspace.policy.widening_requires_approval ||
    policy.egress_profile !== workspace.policy.egress_profile
  )
  const save = async () => {
    if (!workspace || !policy || !dirty) return
    setBusy(true)
    try {
      await updateWorkspacePolicy(workspace.share_id, {
        default_visibility: policy.default_visibility,
        agent_visibility_ceiling: policy.agent_visibility_ceiling,
        widening_requires_approval: policy.widening_requires_approval,
        egress_profile: policy.egress_profile,
        allow_remote_evidence: false,
      })
      toast.success('Workspace sharing policy updated')
      onOpenChange(false)
      onSaved()
    } catch (err) { toast.error(err instanceof Error ? err.message : 'Could not update policy') } finally { setBusy(false) }
  }
  return (
    <Dialog open={workspace !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>{workspace ? workspaceLabel(workspace) : ''} sharing policy</DialogTitle><DialogDescription>Published tasks receive this home-controlled default. Local visibility tools may choose a narrower audience.</DialogDescription></DialogHeader>
        {policy ? <div className="space-y-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2"><Label>Default task visibility</Label><Select value={policy.default_visibility} onValueChange={(value) => setPolicy({ ...policy, default_visibility: value as 'private' | 'workspace' })}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="private">Private</SelectItem><SelectItem value="workspace">Whole workspace</SelectItem></SelectContent></Select></div>
            <div className="space-y-2"><Label>Agent visibility ceiling</Label><Select value={policy.agent_visibility_ceiling} onValueChange={(value) => setPolicy({ ...policy, agent_visibility_ceiling: value as WorkspacePublicationPolicy['agent_visibility_ceiling'] })}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="private">Private only</SelectItem><SelectItem value="restricted">Named people</SelectItem><SelectItem value="workspace">Whole workspace</SelectItem></SelectContent></Select></div>
          </div>
          <label className="flex items-start gap-3 border border-border p-3"><Checkbox checked={policy.widening_requires_approval} onCheckedChange={(value) => setPolicy({ ...policy, widening_requires_approval: value === true })} /><span><span className="block text-xs font-medium">Require approval to widen an existing task</span><span className="mt-0.5 block text-[10px] text-muted-foreground">Prevents a remote update from silently expanding an audience.</span></span></label>
          <div className="flex items-start gap-3 border border-border bg-muted/15 p-3"><LockKeyhole className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" /><span><span className="block text-xs font-medium">Evidence stays on the source device</span><span className="mt-0.5 block text-[10px] leading-4 text-muted-foreground">This wire version shares only sanitized task fields. Attachments, log samples, notes and evidence are never included.</span></span></div>
          <div className="space-y-2"><Label htmlFor="egress-profile">Egress profile</Label><Input id="egress-profile" value={policy.egress_profile} readOnly className="font-mono text-xs" /></div>
        </div> : null}
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button><Button onClick={save} disabled={busy || !dirty}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Check className="h-4 w-4" />}Save policy</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function IdentityRow({ principal, onChanged, onInviteCreated }: { principal: CollaborationPrincipal; onChanged: () => void; onInviteCreated: (result: InvitationResult) => void }) {
  const [busy, setBusy] = useState<string | null>(null)
  const [rotateKey, setRotateKey] = useState<PrincipalIdentityKey | null>(null)
  const { confirm, dialog: confirmDialog } = useConfirm()
  const activeKey = principal.keys.find((key) => key.status === 'active')
  const addDevice = async () => {
    if (!activeKey) return
    setBusy('invite')
    try {
      const result = await createCollaborationInvitation({ purpose: 'add_device', principal_id: principal.id, public_key: activeKey.canonical_public_key })
      onInviteCreated(result)
    } catch (err) { toast.error(err instanceof Error ? err.message : 'Could not create device invitation') } finally { setBusy(null) }
  }
  const removeDevice = async (peerId: string) => {
    const device = principal.devices.find((item) => item.peer_id === peerId)
    const confirmed = await confirm({
      title: `Revoke ${device?.display_name || 'this device'}?`,
      description: `This peer loses future access immediately at this home. Other active devices for ${principal.display_name} remain authorized. Content already received by this device cannot be erased remotely.`,
      confirmLabel: 'Revoke device',
      variant: 'destructive',
    })
    if (!confirmed) return
    setBusy(peerId)
    try { await revokeDevice(peerId, 'revoked by local workspace owner'); toast.success('Device revoked'); onChanged() } catch (err) { toast.error(err instanceof Error ? err.message : 'Could not revoke device') } finally { setBusy(null) }
  }
  const removePrincipal = async () => {
    const noun = principal.kind === 'machine' ? 'machine' : 'person'
    const confirmed = await confirm({
      title: `Revoke ${principal.display_name}?`,
      description: `This revokes every device and workspace grant for this ${noun}. Future home reads and writes stop immediately; previously received content cannot be erased remotely.`,
      confirmLabel: `Revoke ${noun}`,
      variant: 'destructive',
    })
    if (!confirmed) return
    setBusy('principal')
    try { await revokePrincipal(principal.id, 'revoked by local workspace owner'); toast.success('Principal and all access revoked'); onChanged() } catch (err) { toast.error(err instanceof Error ? err.message : 'Could not revoke principal') } finally { setBusy(null) }
  }
  const removeKey = async (key: PrincipalIdentityKey) => {
    const activeKeyCount = principal.keys.filter((item) => item.status === 'active').length
    const confirmed = await confirm({
      title: `Revoke ${key.fingerprint}?`,
      description: `Every device bound through this key loses future access immediately, and pending invitations pinned to it become unusable.${principal.is_local_owner && activeKeyCount === 1 ? ' This device must enroll another SSH identity before it can invite collaborators again.' : ''} Previously received content cannot be erased remotely.`,
      confirmLabel: 'Revoke key',
      variant: 'destructive',
    })
    if (!confirmed) return
    setBusy(`key:${key.id}`)
    try { await revokeIdentityKey(key.id); toast.success('Identity key and its devices revoked'); onChanged() } catch (err) { toast.error(err instanceof Error ? err.message : 'Could not revoke identity key') } finally { setBusy(null) }
  }
  return (
    <div className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <PrincipalLabel principal={principal} />
        <div className="flex gap-2">
          {!principal.is_local_owner && principal.status === 'active' ? <Button size="sm" variant="outline" onClick={addDevice} disabled={!activeKey || busy !== null}>{busy === 'invite' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Laptop className="h-3.5 w-3.5" />}Add device</Button> : null}
          {!principal.is_local_owner && principal.status !== 'revoked' ? <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={removePrincipal} disabled={busy !== null}>Revoke {principal.kind === 'machine' ? 'machine' : 'person'}</Button> : null}
        </div>
      </div>
      <div className="mt-3 grid gap-2 md:grid-cols-2">
        {principal.devices.map((device) => (
          <div key={device.id} className="flex items-center justify-between gap-3 border border-border bg-muted/10 px-3 py-2">
            <div className="min-w-0"><div className="flex items-center gap-2 text-xs"><Laptop className="h-3.5 w-3.5 text-muted-foreground" /><span className="truncate">{device.display_name}</span><Badge variant="outline" tone={device.status === 'active' ? 'success' : 'muted'} className="text-[9px]">{device.status.replace('_', ' ')}</Badge></div><div className="mt-1 truncate font-mono text-[9px] text-muted-foreground">peer:{device.peer_id.slice(-12)}</div></div>
            {device.status === 'active' && !(principal.is_local_owner && principal.devices.filter((item) => item.status === 'active').length === 1) ? <Button size="sm" variant="ghost" className="h-7 text-[10px] text-destructive hover:text-destructive" onClick={() => removeDevice(device.peer_id)} disabled={busy !== null}>Revoke</Button> : null}
          </div>
        ))}
        {principal.devices.length === 0 ? <div className="border border-dashed border-border px-3 py-2 text-[11px] text-muted-foreground">No verified devices yet.</div> : null}
      </div>
      {principal.keys.length > 0 ? <div className="mt-3 divide-y divide-border border border-border">{principal.keys.map((key) => (
        <div key={key.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
          <span className="font-mono text-[9px] text-muted-foreground">{key.status} · {key.fingerprint}</span>
          {key.status === 'active' && principal.status === 'active' ? <div className="flex gap-1">
            <Button size="sm" variant="ghost" className="h-7 text-[10px]" onClick={() => setRotateKey(key)} disabled={busy !== null}>Rotate</Button>
            <Button size="sm" variant="ghost" className="h-7 text-[10px] text-destructive hover:text-destructive" onClick={() => removeKey(key)} disabled={busy !== null}>{busy === `key:${key.id}` ? <Loader2 className="h-3 w-3 animate-spin" /> : null}Revoke</Button>
          </div> : null}
        </div>
      ))}</div> : null}
      <RotateKeyDialog principal={principal} replacedKey={rotateKey} onOpenChange={(open) => { if (!open) setRotateKey(null) }} onCreated={(result) => { setRotateKey(null); onInviteCreated(result) }} />
      {confirmDialog}
    </div>
  )
}

function RotateKeyDialog({ principal, replacedKey, onOpenChange, onCreated }: { principal: CollaborationPrincipal; replacedKey: PrincipalIdentityKey | null; onOpenChange: (open: boolean) => void; onCreated: (result: InvitationResult) => void }) {
  const [publicKey, setPublicKey] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!replacedKey) setPublicKey('') }, [replacedKey])
  const submit = async () => {
    if (!replacedKey || !publicKey.trim()) return
    setBusy(true)
    try {
      const result = await createCollaborationInvitation({
        purpose: 'rotate_key', principal_id: principal.id,
        public_key: publicKey.trim(), replaces_key_id: replacedKey.id,
      })
      setPublicKey('')
      onCreated(result)
    } catch (err) { toast.error(err instanceof Error ? err.message : 'Could not create key-rotation invitation') } finally { setBusy(false) }
  }
  return (
    <Dialog open={replacedKey !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>Rotate {principal.display_name}’s SSH identity</DialogTitle><DialogDescription>The new Ed25519 key must be loaded in that device’s ssh-agent. Join with the one-time result to rebind the same device, then revoke the old key after every intended device has moved.</DialogDescription></DialogHeader>
        <div className="space-y-3">
          <div className="border border-border bg-muted/15 px-3 py-2 font-mono text-[10px] text-muted-foreground">replaces {replacedKey?.fingerprint}</div>
          <div className="space-y-2"><Label htmlFor={`rotate-key-${principal.id}`}>New OpenSSH public key</Label><Textarea id={`rotate-key-${principal.id}`} value={publicKey} onChange={(event) => setPublicKey(event.target.value)} rows={3} className="font-mono text-xs" placeholder="ssh-ed25519 AAAAC3…" /></div>
        </div>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button><Button onClick={submit} disabled={busy || !publicKey.trim()}>{busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <KeyRound className="h-4 w-4" />}Create rotation invite</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
