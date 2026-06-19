import { useCallback, useState } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
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
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useApi } from '@/hooks/use-api'
import {
  createWorkspaceLink,
  deleteWorkspaceLink,
  listWorkspaceLinks,
  suggestWorkspaceLinks,
} from '@/api/client'
import type { WorkspaceLink, WorkspaceLinkSuggestion } from '@/api/types'
import { Link2, Plus, Trash2 } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'

interface FormData {
  peer_id: string
  local_workspace: string
  remote_workspace_id: string
  remote_workspace_name: string
}

const emptyForm: FormData = {
  peer_id: '',
  local_workspace: '',
  remote_workspace_id: '',
  remote_workspace_name: '',
}

export function LinkedWorkspacesPage() {
  const fetcher = useCallback(() => listWorkspaceLinks(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [form, setForm] = useState<FormData>(emptyForm)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<WorkspaceLink | null>(null)

  function openCreate() {
    setForm(emptyForm)
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleSave() {
    setSaving(true)
    setSaveError(null)
    try {
      const res = await createWorkspaceLink({
        peer_id: form.peer_id,
        local_workspace: form.local_workspace,
        remote_workspace_id: form.remote_workspace_id,
        remote_workspace_name: form.remote_workspace_name || undefined,
      })
      setDialogOpen(false)
      toast.success('Workspace linked', {
        description: res.scope_grant_warning
          ? res.scope_grant_warning
          : `Granted scope: ${res.granted_scope}`,
      })
      refetch()
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to link workspace')
    } finally {
      setSaving(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    try {
      await deleteWorkspaceLink(deleteTarget.peer_id, deleteTarget.remote_workspace_id)
      setDeleteTarget(null)
      toast.success('Workspace unlinked')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to unlink workspace')
    }
  }

  function shortPeer(peerId: string) {
    if (peerId.length <= 16) return peerId
    return `${peerId.slice(0, 8)}…${peerId.slice(-6)}`
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Linked workspaces keep a workspace in sync across paired machines — tasks created here
          replicate to the linked peer's workspace.
        </p>
        <Button onClick={openCreate} data-testid="workspace-link-add">
          <Plus className="mr-2 h-4 w-4" />
          Link workspace
        </Button>
      </div>

      <Card>
        <CardContent className="pt-6">
          {loading && !data && (
            <div className="flex items-center gap-2 text-muted-foreground">
              <div className="h-2 w-2 rounded-full bg-primary/60" />
              Loading...
            </div>
          )}
          {error && <p className="text-destructive">Error: {error}</p>}
          {data && (
            <Table>
              <TableHeader>
                <TableRow className="border-border/50 hover:bg-transparent">
                  <TableHead>Local Workspace</TableHead>
                  <TableHead>→ Peer</TableHead>
                  <TableHead>Remote Workspace</TableHead>
                  <TableHead className="hidden lg:table-cell">Established By</TableHead>
                  <TableHead className="w-24">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="h-32">
                      <div className="flex flex-col items-center justify-center text-muted-foreground">
                        <Link2 className="mb-2 h-8 w-8 text-muted-foreground/50" />
                        <p className="text-sm">No linked workspaces</p>
                        <button
                          onClick={openCreate}
                          className="text-xs text-primary hover:underline"
                        >
                          Link a workspace to a paired peer
                        </button>
                      </div>
                    </TableCell>
                  </TableRow>
                ) : (
                  data.map((link) => (
                    <TableRow
                      key={`${link.peer_id}:${link.remote_workspace_id}`}
                      className="border-border/30 hover:bg-muted/30"
                    >
                      <TableCell className="font-medium">
                        {link.local_workspace_name || link.local_workspace_id}
                      </TableCell>
                      <TableCell>
                        <span className="font-mono text-xs text-accent-foreground" title={link.peer_id}>
                          {shortPeer(link.peer_id)}
                        </span>
                      </TableCell>
                      <TableCell>
                        {link.remote_workspace_name || link.remote_workspace_id}
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        <span className="text-muted-foreground">
                          {link.link_established_by || '-'}
                        </span>
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8 w-8 p-0 hover:bg-destructive/10 hover:text-destructive"
                                aria-label="Unlink workspace"
                                data-testid={`workspace-link-delete-${link.peer_id}-${link.remote_workspace_id}`}
                                onClick={() => setDeleteTarget(link)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Unlink</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <LinkDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={handleSave}
        saving={saving}
        saveError={saveError}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Unlink workspace"
        description={`Stop replicating tasks to "${
          deleteTarget?.remote_workspace_name || deleteTarget?.remote_workspace_id
        }" on the linked peer?`}
        confirmLabel="Unlink"
        variant="destructive"
        onConfirm={confirmDelete}
      />
    </div>
  )
}

function LinkDialog({
  open,
  onClose,
  form,
  setForm,
  onSave,
  saving,
  saveError,
}: {
  open: boolean
  onClose: () => void
  form: FormData
  setForm: React.Dispatch<React.SetStateAction<FormData>>
  onSave: () => void
  saving: boolean
  saveError: string | null
}) {
  // Suggestions help the operator fill the form — fetched on every open.
  const suggestFetcher = useCallback(() => suggestWorkspaceLinks(), [])
  const { data: suggestData } = useApi(suggestFetcher)
  const suggestions = suggestData?.suggestions ?? []

  function applySuggestion(s: WorkspaceLinkSuggestion) {
    setForm({
      peer_id: s.peer_id,
      local_workspace: s.local_workspace_id,
      remote_workspace_id: s.remote_workspace_id,
      remote_workspace_name: s.remote_workspace_name ?? '',
    })
  }

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Link workspace</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          {suggestions.length > 0 && (
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Suggestions</Label>
              <div className="space-y-1">
                {suggestions.map((s) => (
                  <button
                    key={`${s.peer_id}:${s.remote_workspace_id}`}
                    type="button"
                    onClick={() => applySuggestion(s)}
                    data-testid={`workspace-link-suggestion-${s.peer_id}-${s.remote_workspace_id}`}
                    className="flex w-full items-center justify-between gap-2 rounded-md border border-border/50 bg-card/40 px-3 py-2 text-left text-xs transition-colors hover:border-border hover:bg-muted/40"
                  >
                    <span className="truncate font-medium">
                      {s.local_workspace_name || s.local_workspace_id}
                      <span className="mx-1 text-muted-foreground">→</span>
                      {s.remote_workspace_name || s.remote_workspace_id}
                    </span>
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                      {s.peer_id.length <= 16 ? s.peer_id : `${s.peer_id.slice(0, 8)}…`}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          )}
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Peer ID</Label>
            <Input
              className="font-mono text-sm"
              value={form.peer_id}
              onChange={(e) => setForm((f) => ({ ...f, peer_id: e.target.value }))}
              placeholder="12D3Koo…"
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Local workspace (id or name)</Label>
            <Input
              value={form.local_workspace}
              onChange={(e) => setForm((f) => ({ ...f, local_workspace: e.target.value }))}
              placeholder="my-workspace"
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Remote workspace id</Label>
            <Input
              className="font-mono text-sm"
              value={form.remote_workspace_id}
              onChange={(e) => setForm((f) => ({ ...f, remote_workspace_id: e.target.value }))}
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Remote workspace name (optional)</Label>
            <Input
              value={form.remote_workspace_name}
              onChange={(e) =>
                setForm((f) => ({ ...f, remote_workspace_name: e.target.value }))
              }
            />
          </div>
        </div>
        {saveError && <p className="text-sm text-destructive">{saveError}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose} data-testid="workspace-link-cancel">
            Cancel
          </Button>
          <Button
            onClick={onSave}
            disabled={saving || !form.peer_id || !form.local_workspace || !form.remote_workspace_id}
            data-testid="workspace-link-save"
          >
            {saving ? 'Linking...' : 'Link'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
