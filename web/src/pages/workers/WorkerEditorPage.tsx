// WorkerEditorPage (M0.6) — create + edit modes share one component.
// /workers/new boots from defaultState; /workers/:id/edit boots from
// the worker's current config. Save → POST or PATCH; navigate back to
// the detail page on success. State + side effects live here; the
// visual layout is delegated to WorkerEditorTabs.

import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { Loader2, Save } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/use-api'
import { listAuthScopes, listSkillRegistry, listWorkspaces } from '@/api/client'
import { createWorker, getWorker, listTools, updateWorker } from '@/api/workers'
import {
  defaultState,
  stateFromWorker,
  toCreateInput,
  toUpdateInput,
  validateState,
  type EditorState,
} from './worker-editor-state'
import { WorkerEditorTabs } from './WorkerEditorTabs'

export function WorkerEditorPage() {
  const { id } = useParams<{ id?: string }>()
  const isEdit = Boolean(id)
  const navigate = useNavigate()

  const fetcher = useCallback(() => (id ? getWorker(id) : Promise.resolve(null)), [id])
  const { data: loaded, loading: loadingExisting, error: loadError } = useApi(fetcher)

  const scopesFetcher = useCallback(() => listAuthScopes(), [])
  const { data: authScopes, refetch: refetchScopes } = useApi(scopesFetcher)
  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)
  // Tool catalogue + skill registry power the checkbox grid in
  // ToolsCard and the autocomplete in SkillCard. Both endpoints are
  // best-effort: empty / failed responses fall through to the
  // JSON-textarea + free-text input fallbacks so the editor still
  // saves a valid Worker.
  const toolsFetcher = useCallback(() => listTools(), [])
  const { data: tools } = useApi(toolsFetcher)
  const skillsFetcher = useCallback(() => listSkillRegistry(), [])
  const { data: skills } = useApi(skillsFetcher)

  const [state, setState] = useState<EditorState>(defaultState)
  const [saving, setSaving] = useState(false)
  const [hydrated, setHydrated] = useState(false)

  // Seed defaults: when creating, pick the first workspace as soon as
  // the list arrives so the user doesn't have to.
  useEffect(() => {
    if (!isEdit && workspaces && workspaces.length > 0 && !state.workspaceID) {
      setState((s) => ({
        ...s,
        workspaceID: workspaces[0].id,
        workspaceAccess: [{ workspace_id: workspaces[0].id, access: 'write' }],
      }))
    }
  }, [isEdit, workspaces, state.workspaceID])

  // Hydrate from existing worker on edit.
  useEffect(() => {
    if (isEdit && loaded?.worker && !hydrated) {
      setState(stateFromWorker(loaded.worker))
      setHydrated(true)
    }
  }, [isEdit, loaded, hydrated])

  const set = useCallback(
    <K extends keyof EditorState>(key: K, value: EditorState[K]) => {
      setState((s) => ({ ...s, [key]: value }))
    },
    [],
  )

  async function handleSave() {
    const err = validateState(state)
    if (err) {
      toast.error(err)
      return
    }
    setSaving(true)
    try {
      if (isEdit && id) {
        await updateWorker(id, toUpdateInput(state))
        toast.success('Worker saved')
        navigate(`/workers/${id}`)
      } else {
        const created = await createWorker(toCreateInput(state))
        toast.success(`Worker "${created.name}" created`)
        navigate(`/workers/${created.id}`)
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  // Save is disabled until every collaborator endpoint has resolved
  // (workspaces, authScopes, tools). Without this guard, toUpdateInput
  // would silently send the unhydrated workspace_id="" — a partial
  // save that drops the worker out of its workspace.
  const collaboratorsReady = workspaces !== null && authScopes !== null && tools !== null
  const saveDisabled = saving || !collaboratorsReady

  if (isEdit && loadingExisting && !loaded) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> Loading worker…
      </div>
    )
  }
  if (isEdit && loadError) {
    return (
      <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
        {loadError}
      </div>
    )
  }

  return (
    <div className="space-y-5 max-w-3xl">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">
            {isEdit ? `Edit ${state.name || 'worker'}` : 'New worker'}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Define the agent's identity, model, schedule, and output sinks. Saved
            workers are runnable immediately when enabled.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" asChild>
            <Link to={isEdit && id ? `/workers/${id}` : '/workers'}>Cancel</Link>
          </Button>
          <Button onClick={handleSave} disabled={saveDisabled} data-testid="worker-save">
            {saving || !collaboratorsReady ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Save className="mr-1.5 h-3.5 w-3.5" />
            )}
            {collaboratorsReady ? 'Save' : 'Loading…'}
          </Button>
        </div>
      </header>

      <WorkerEditorTabs
        state={state}
        set={set}
        workspaces={workspaces}
        authScopes={authScopes}
        tools={tools}
        skills={skills}
        onSecretCreated={() => refetchScopes()}
      />
    </div>
  )
}
