import { useEffect, useMemo, useState } from 'react'
import { Plus, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { createWorkspace, deleteWorkspace, updateWorkspace } from '@/api/client'
import type { Workspace } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface WorkspaceForm {
  name: string
  root_path: string
  default_policy: 'allow' | 'deny'
  tags: Record<string, string>
}

const EMPTY_FORM: WorkspaceForm = {
  name: '',
  root_path: '',
  default_policy: 'allow',
  tags: {},
}

function formForWorkspace(workspace: Workspace | null): WorkspaceForm {
  if (!workspace) return { ...EMPTY_FORM, tags: {} }
  return {
    name: workspace.name,
    root_path: workspace.root_path,
    default_policy: workspace.default_policy,
    tags: { ...(workspace.tags ?? {}) },
  }
}

export function WorkspaceSettingsPanel({
  workspace,
  onSaved,
  onDeleted,
  onCancel,
}: {
  workspace: Workspace | null
  onSaved: (workspace: Workspace) => void
  onDeleted: (workspaceId: string) => void
  onCancel?: () => void
}) {
  const [form, setForm] = useState<WorkspaceForm>(() => formForWorkspace(workspace))
  const [tagKey, setTagKey] = useState('')
  const [tagValue, setTagValue] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  useEffect(() => {
    setForm(formForWorkspace(workspace))
    setTagKey('')
    setTagValue('')
    setSaveError(null)
  }, [workspace])

  const dirty = useMemo(
    () => JSON.stringify(form) !== JSON.stringify(formForWorkspace(workspace)),
    [form, workspace],
  )

  function addTag() {
    const key = tagKey.trim()
    if (!key) return
    setForm((current) => ({
      ...current,
      tags: { ...current.tags, [key]: tagValue.trim() },
    }))
    setTagKey('')
    setTagValue('')
  }

  function removeTag(key: string) {
    setForm((current) => {
      const tags = { ...current.tags }
      delete tags[key]
      return { ...current, tags }
    })
  }

  async function save() {
    setSaving(true)
    setSaveError(null)
    try {
      const saved = workspace
        ? await updateWorkspace(workspace.id, form)
        : await createWorkspace(form)
      toast.success(workspace ? 'Workspace updated' : 'Workspace created')
      onSaved(saved)
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : 'Failed to save workspace')
    } finally {
      setSaving(false)
    }
  }

  async function remove() {
    if (!workspace) return
    try {
      await deleteWorkspace(workspace.id)
      toast.success('Workspace deleted')
      setConfirmDelete(false)
      onDeleted(workspace.id)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Failed to delete workspace')
    }
  }

  return (
    <section className="border border-border/60 bg-card/20" data-testid="workspace-settings-panel">
      <div className="border-b border-border/60 px-4 py-3">
        <h2 className="text-sm font-semibold">{workspace ? 'Workspace settings' : 'Create workspace'}</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          The root path is the security boundary used to match agent sessions to this workspace.
        </p>
      </div>

      <div className="max-w-2xl space-y-5 p-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="workspace-name">Name</Label>
            <Input
              id="workspace-name"
              value={form.name}
              onChange={(event) => setForm((current) => ({ ...current, name: event.target.value }))}
              placeholder="Product app"
              autoFocus={!workspace}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="workspace-policy">Default policy</Label>
            <Select
              value={form.default_policy}
              onValueChange={(value) => setForm((current) => ({
                ...current,
                default_policy: value as 'allow' | 'deny',
              }))}
            >
              <SelectTrigger id="workspace-policy"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="allow">Allow when a rule matches</SelectItem>
                <SelectItem value="deny">Deny unless explicitly allowed</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="workspace-root">Root path</Label>
          <Input
            id="workspace-root"
            className="font-mono text-sm"
            value={form.root_path}
            onChange={(event) => setForm((current) => ({ ...current, root_path: event.target.value }))}
            placeholder="/Users/you/projects/product-app"
          />
          <p className="text-xs text-muted-foreground">Use the full local path. Nested sessions inherit this workspace.</p>
        </div>

        <div className="space-y-2">
          <Label>Tags</Label>
          {Object.keys(form.tags).length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {Object.entries(form.tags).map(([key, value]) => (
                <Badge key={key} variant="outline" className="gap-1 font-mono text-xs">
                  {key}={value}
                  <button type="button" onClick={() => removeTag(key)} aria-label={`Remove tag ${key}`}>
                    <X className="h-3 w-3" />
                  </button>
                </Badge>
              ))}
            </div>
          )}
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input value={tagKey} onChange={(event) => setTagKey(event.target.value)} placeholder="Key" />
            <Input value={tagValue} onChange={(event) => setTagValue(event.target.value)} placeholder="Value" />
            <Button type="button" variant="outline" onClick={addTag} disabled={!tagKey.trim()}>
              <Plus className="mr-1.5 h-4 w-4" /> Add tag
            </Button>
          </div>
        </div>

        {saveError && <p className="text-sm text-destructive">{saveError}</p>}

        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border/50 pt-4">
          <div>
            {workspace && (
              <Button variant="ghost" className="text-destructive" onClick={() => setConfirmDelete(true)}>
                <Trash2 className="mr-1.5 h-4 w-4" /> Delete workspace
              </Button>
            )}
          </div>
          <div className="flex gap-2">
            {(dirty || !workspace) && onCancel && <Button variant="ghost" onClick={onCancel}>Cancel</Button>}
            {workspace && dirty && (
              <Button variant="outline" onClick={() => setForm(formForWorkspace(workspace))}>Discard</Button>
            )}
            <Button onClick={() => void save()} disabled={saving || !form.name.trim() || (workspace !== null && !dirty)}>
              {saving ? 'Saving…' : workspace ? 'Save changes' : 'Create workspace'}
            </Button>
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete workspace"
        description={`Delete “${workspace?.name ?? ''}”? Access rules tied to it will no longer be usable.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => void remove()}
      />
    </section>
  )
}
