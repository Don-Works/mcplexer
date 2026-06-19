import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
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
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import {
  createWorkspace,
  deleteWorkspace,
  listWorkspaces,
  updateWorkspace,
} from '@/api/client'
import type { Workspace } from '@/api/types'
import { ArrowLeft, FolderOpen, Pencil, Plus, Route, Trash2 } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'

interface FormData {
  name: string
  root_path: string
  default_policy: 'allow' | 'deny'
  tags: Record<string, string>
}

const emptyForm: FormData = {
  name: '',
  root_path: '',
  default_policy: 'allow',
  tags: {},
}

export function WorkspacesPage() {
  const fetcher = useCallback(() => listWorkspaces(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<Workspace | null>(null)
  const [form, setForm] = useState<FormData>(emptyForm)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Workspace | null>(null)

  // Deep link: /workspaces/manage?workspace=<id> from workspace headers and
  // legacy /config?tab=workspaces&workspace=<id> redirects. UI-6 fix — mirror
  // DownstreamsPage's ?server= pattern so the
  // matching workspace row pulses + scrolls into view.
  const [searchParams, setSearchParams] = useSearchParams()
  const [highlightWorkspaceId, setHighlightWorkspaceId] = useState<string | null>(null)
  useEffect(() => {
    const target = searchParams.get('workspace')
    if (!target) return
    setHighlightWorkspaceId(target)
    const scrollTimer = setTimeout(() => {
      const el = document.querySelector<HTMLElement>(`[data-workspace-id="${target}"]`)
      el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 50)
    const clearParamTimer = setTimeout(() => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          next.delete('workspace')
          return next
        },
        { replace: true },
      )
    }, 200)
    const clearHighlightTimer = setTimeout(() => setHighlightWorkspaceId(null), 3500)
    return () => {
      clearTimeout(scrollTimer)
      clearTimeout(clearParamTimer)
      clearTimeout(clearHighlightTimer)
    }
  }, [searchParams, setSearchParams])

  function openCreate() {
    setEditing(null)
    setForm(emptyForm)
    setSaveError(null)
    setDialogOpen(true)
  }

  function openEdit(w: Workspace) {
    setEditing(w)
    setForm({
      name: w.name,
      root_path: w.root_path,
      default_policy: w.default_policy,
      tags: { ...w.tags },
    })
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleSave() {
    setSaving(true)
    setSaveError(null)
    try {
      if (editing) {
        await updateWorkspace(editing.id, form)
      } else {
        await createWorkspace(form)
      }
      setDialogOpen(false)
      toast.success(editing ? 'Workspace updated' : 'Workspace created')
      refetch()
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save workspace')
    } finally {
      setSaving(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    try {
      await deleteWorkspace(deleteTarget.id)
      setDeleteTarget(null)
      toast.success('Workspace deleted')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete workspace')
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div className="space-y-1">
          <Button variant="ghost" size="sm" asChild className="-ml-2 h-8">
            <Link to="/workspaces">
              <ArrowLeft className="mr-1.5 h-4 w-4" />
              Workspaces
            </Link>
          </Button>
          <h1 className="text-xl font-semibold">Workspace Settings</h1>
        </div>
        <Button variant="outline" asChild>
          <Link to="/advanced/routes">
            <Route className="mr-1.5 h-4 w-4" />
            Edit routes
          </Link>
        </Button>
      </div>
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-2xl text-sm text-muted-foreground">
          A workspace is a directory tree paired with a default route policy. Routes match incoming
          tool calls against the workspace's root path; the default policy decides what happens
          when no rule matches.
        </p>
        <Button onClick={openCreate} data-testid="workspace-add">
          <Plus className="mr-2 h-4 w-4" />
          Add Workspace
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
                  <TableHead>Name</TableHead>
                  <TableHead className="hidden sm:table-cell">Root Path</TableHead>
                  <TableHead>Default Policy</TableHead>
                  <TableHead className="hidden lg:table-cell">Tags</TableHead>
                  <TableHead className="w-32">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="h-32">
                      <div className="flex flex-col items-center justify-center text-muted-foreground">
                        <FolderOpen className="mb-2 h-8 w-8 text-muted-foreground/50" />
                        <p className="text-sm">No workspaces configured</p>
                        <button onClick={openCreate} className="text-xs text-primary hover:underline">
                          Add a workspace to get started
                        </button>
                      </div>
                    </TableCell>
                  </TableRow>
                ) : (
                  data.map((w) => (
                    <TableRow
                      key={w.id}
                      data-workspace-id={w.id}
                      className={
                        'border-border/30 hover:bg-muted/30 ' +
                        (highlightWorkspaceId === w.id
                          ? 'ring-2 ring-amber-400/80 ring-offset-2 ring-offset-background'
                          : '')
                      }
                    >
                      <TableCell className="font-medium">{w.name}</TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <div className="max-w-[22rem] truncate font-mono text-xs text-accent-foreground" title={w.root_path}>
                          {w.root_path}
                        </div>
                      </TableCell>
                      <TableCell>
                        <PolicyBadge policy={w.default_policy} />
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        <TagsList tags={w.tags} />
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8 w-8 p-0"
                                aria-label={`Edit routes for ${w.name}`}
                                data-testid={`workspace-routes-${w.id}`}
                                asChild
                              >
                                <Link to={`/advanced/routes?workspace=${encodeURIComponent(w.id)}`}>
                                  <Route className="h-3.5 w-3.5" />
                                </Link>
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Routes</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8 w-8 p-0"
                                aria-label={`Edit ${w.name}`}
                                data-testid={`workspace-edit-${w.id}`}
                                onClick={() => openEdit(w)}
                              >
                                <Pencil className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Edit</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8 w-8 p-0 hover:bg-destructive/10 hover:text-destructive"
                                aria-label={`Delete ${w.name}`}
                                data-testid={`workspace-delete-${w.id}`}
                                onClick={() => setDeleteTarget(w)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Delete</TooltipContent>
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

      <WorkspaceDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={handleSave}
        saving={saving}
        editing={!!editing}
        saveError={saveError}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Delete workspace"
        description={`Are you sure you want to delete "${deleteTarget?.name}"?`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={confirmDelete}
      />
    </div>
  )
}

function PolicyBadge({ policy }: { policy: 'allow' | 'deny' }) {
  return (
    <Badge variant={policy === 'allow' ? 'secondary' : 'destructive'}>
      {policy}
    </Badge>
  )
}

function TagsList({ tags }: { tags: Record<string, string> }) {
  const entries = Object.entries(tags ?? {})
  if (entries.length === 0) return <span className="text-muted-foreground">-</span>
  return (
    <div className="flex flex-wrap gap-1">
      {entries.map(([k, v]) => (
        <Badge key={k} variant="outline" className="font-mono text-xs">
          {k}={v}
        </Badge>
      ))}
    </div>
  )
}

function WorkspaceDialog({
  open,
  onClose,
  form,
  setForm,
  onSave,
  saving,
  editing,
  saveError,
}: {
  open: boolean
  onClose: () => void
  form: FormData
  setForm: React.Dispatch<React.SetStateAction<FormData>>
  onSave: () => void
  saving: boolean
  editing: boolean
  saveError: string | null
}) {
  const [tagKey, setTagKey] = useState('')
  const [tagValue, setTagValue] = useState('')

  function addTag() {
    if (!tagKey) return
    setForm((f) => ({ ...f, tags: { ...f.tags, [tagKey]: tagValue } }))
    setTagKey('')
    setTagValue('')
  }

  function removeTag(key: string) {
    setForm((f) => {
      const tags = { ...f.tags }
      delete tags[key]
      return { ...f, tags }
    })
  }

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? 'Edit Workspace' : 'Add Workspace'}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Name</Label>
            <Input
              value={form.name}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Root Path</Label>
            <Input
              className="font-mono text-sm"
              value={form.root_path}
              onChange={(e) => setForm((f) => ({ ...f, root_path: e.target.value }))}
              placeholder="/path/to/workspace"
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Default Policy</Label>
            <Select
              value={form.default_policy}
              onValueChange={(v) =>
                setForm((f) => ({ ...f, default_policy: v as 'allow' | 'deny' }))
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="allow">Allow</SelectItem>
                <SelectItem value="deny">Deny</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Tags</Label>
            <div className="flex flex-wrap gap-1 mb-2">
              {Object.entries(form.tags).map(([k, v]) => (
                <Badge
                  key={k}
                  variant="outline"
                  role="button"
                  tabIndex={0}
                  aria-label={`Remove tag ${k}=${v}`}
                  className="cursor-pointer font-mono text-xs hover:bg-destructive/10 hover:text-destructive focus-visible:ring-1 focus-visible:ring-destructive"
                  onClick={() => removeTag(k)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      removeTag(k)
                    }
                  }}
                >
                  {k}={v} x
                </Badge>
              ))}
            </div>
            <div className="flex gap-2">
              <Input
                placeholder="Key"
                value={tagKey}
                onChange={(e) => setTagKey(e.target.value)}
                className="flex-1"
              />
              <Input
                placeholder="Value"
                value={tagValue}
                onChange={(e) => setTagValue(e.target.value)}
                className="flex-1"
              />
              <Button type="button" variant="outline" size="sm" onClick={addTag}>
                Add
              </Button>
            </div>
          </div>
        </div>
        {saveError && (
          <p className="text-sm text-destructive">{saveError}</p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose} data-testid="workspace-cancel">
            Cancel
          </Button>
          <Button onClick={onSave} disabled={saving || !form.name} data-testid="workspace-save">
            {saving ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
