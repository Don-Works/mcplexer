import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useApi } from '@/hooks/use-api'
import {
  getBrainClients,
  getBrainWorkspaces,
  getBrainScope,
  getBrainTask,
  getBrainMemory,
  type BrainTaskRecord,
  type BrainMemoryRecord,
} from '@/api/brainBrowser'
import { Brain, RefreshCw, X } from 'lucide-react'
import { BrainRecordEditor } from './BrainRecordEditor'
import { RecordList, type RecordTab } from './components/RecordList'
import { ScopePicker } from './components/ScopePicker'
import { ScopeFusionLine } from './components/ScopeFusionLine'
import { RecordListSkeleton } from './components/RecordSkeleton'
import {
  BrainLoadingSkeleton,
  BrainErrorCard,
  BrainEmptyIndexCard,
} from './components/BrainBrowserStates'
import { useBrainBrowserRecords } from './useBrainBrowserRecords'

type Selection =
  | { kind: 'task'; rec: BrainTaskRecord }
  | { kind: 'memory'; rec: BrainMemoryRecord }
  | null

// emptyTask / emptyMemory build skeleton records for the "New" flow. A new
// memory's kind follows the active tab: the Memories tab seeds a fact, the
// Notes tab seeds a note (DESIGN §2 — facts are auto-recalled, notes are
// free-form scratch).
function emptyTask(ws: string): BrainTaskRecord {
  return { id: '', workspace: ws, title: '', status: 'open', tags: [], pinned: false, description: '' }
}
function emptyMemory(ws: string, kind: 'note' | 'fact'): BrainMemoryRecord {
  return { id: '', kind, name: '', workspace: ws, tags: [], pinned: false, content: '' }
}

function tabFromKindParam(kind?: string): RecordTab {
  if (kind === 'memory') return 'memory'
  if (kind === 'tasks') return 'tasks'
  // Default to Notes: the brain opens on the surface a person is most likely
  // to write in, not the agent-operational task list.
  return 'notes'
}

export function BrainBrowserPage() {
  const navigate = useNavigate()
  // Deep-link params: /brain/browse/:ws/:kind/:id (DESIGN §2). Absent on the
  // bare /brain/browse route.
  const params = useParams<{ ws?: string; kind?: string; id?: string }>()
  // Query params drive the cmd+K command-surface verbs (DESIGN §4.0):
  //   ?new=task|memory  -> open the New flow, ?ws=<id> -> switch scope,
  //   ?tag=<t> -> filter the lists to a tag.
  const [searchParams, setSearchParams] = useSearchParams()
  const newParam = searchParams.get('new')
  const wsParam = searchParams.get('ws')
  const tagParam = searchParams.get('tag')

  const clientsFetcher = useCallback(() => getBrainClients(), [])
  const { data: clients, loading: clientsLoading, error: clientsErr } = useApi(clientsFetcher)

  const wsFetcher = useCallback(() => getBrainWorkspaces(), [])
  const { data: workspaces, loading: wsLoading, error: wsErr, refetch: refetchWorkspaces } =
    useApi(wsFetcher)

  const [client, setClient] = useState<string | null>(null)
  const [ws, setWs] = useState<string | null>(params.ws ?? null)
  const [source, setSource] = useState<'central' | 'repo' | null>(null)
  const [tab, setTab] = useState<RecordTab>(tabFromKindParam(params.kind))
  const [statusFilter, setStatusFilter] = useState<string | null>(null)
  const [selection, setSelection] = useState<Selection>(null)
  // listKey bumps to force a list refetch after a save.
  const [listKey, setListKey] = useState(0)

  const loading = clientsLoading || wsLoading

  // Default-workspace selection — derived in an effect, never written during
  // render (resolves the prior setState-during-render anti-pattern).
  useEffect(() => {
    if (ws === null && workspaces && workspaces.length > 0) {
      setWs(params.ws ?? workspaces[0].id)
    }
  }, [ws, workspaces, params.ws])

  const {
    tasks,
    memories,
    notes,
    allMemories,
    listLoading,
    error: recordsErr,
  } = useBrainBrowserRecords({
    workspace: ws,
    source,
    activeTab: tab,
    refreshKey: listKey,
    tag: tagParam,
  })
  const error = clientsErr || wsErr || recordsErr

  const scopeFetcher = useCallback(
    () => (ws ? getBrainScope(ws) : Promise.resolve({ scope: '' })),
    [ws],
  )
  const { data: scope } = useApi(scopeFetcher)

  // Deep-link load: when the URL carries /:ws/:kind/:id, fetch that record's
  // detail (raw .md + on_disk_hash) and open it. Wires getBrainTask/Memory.
  useEffect(() => {
    if (!params.id || !params.kind) return
    const k = params.kind === 'memory' || params.kind === 'notes' ? 'memory' : 'task'
    const loader = k === 'task' ? getBrainTask(params.id) : getBrainMemory(params.id)
    loader
      .then((rec) =>
        setSelection(
          k === 'task'
            ? { kind: 'task', rec: rec as BrainTaskRecord }
            : { kind: 'memory', rec: rec as BrainMemoryRecord },
        ),
      )
      .catch(() => {
        /* missing record — leave the list view */
      })
  }, [params.id, params.kind])

  // ?ws=<id> from cmd+K "> switch scope" / "@workspace": switch the active
  // workspace, then clear the param so a refresh doesn't re-trigger.
  useEffect(() => {
    if (!wsParam) return
    setWs(wsParam)
    setSelection(null)
    setSearchParams((p) => {
      p.delete('ws')
      return p
    }, { replace: true })
  }, [wsParam, setSearchParams])

  // ?new=task|memory from cmd+K "> new task / > new memory": open the New flow
  // once the workspace is known, then clear the param.
  useEffect(() => {
    if (!newParam || !ws) return
    if (newParam === 'task') {
      setTab('tasks')
      setSelection({ kind: 'task', rec: emptyTask(ws) })
    } else {
      setTab('memory')
      setSelection({ kind: 'memory', rec: emptyMemory(ws, 'fact') })
    }
    setSearchParams((p) => {
      p.delete('new')
      return p
    }, { replace: true })
  }, [newParam, ws, setSearchParams])

  const selectedId = selection ? selection.rec.id || '__new__' : null

  // openRecord fetches full detail (raw .md + on_disk_hash for CAS) rather than
  // reusing the lightweight list row, and deep-links the URL.
  const openRecord = useCallback(
    (kind: 'task' | 'memory', id: string) => {
      const loader = kind === 'task' ? getBrainTask(id) : getBrainMemory(id)
      loader
        .then((rec) => {
          setSelection(
            kind === 'task'
              ? { kind: 'task', rec: rec as BrainTaskRecord }
              : { kind: 'memory', rec: rec as BrainMemoryRecord },
          )
          if (ws) {
            const urlKind = kind === 'task' ? 'tasks' : 'memory'
            navigate(`/brain/browse/${encodeURIComponent(ws)}/${urlKind}/${encodeURIComponent(id)}`)
          }
        })
        .catch(() => {
          // Fall back to the list row if the detail read fails.
          if (kind === 'task') {
            const r = (tasks ?? []).find((t) => t.id === id)
            if (r) setSelection({ kind: 'task', rec: r })
          } else {
            const r = (allMemories ?? []).find((m) => m.id === id)
            if (r) setSelection({ kind: 'memory', rec: r })
          }
        })
    },
    [tasks, allMemories, ws, navigate],
  )

  function startNew() {
    if (!ws) return
    if (tab === 'tasks') {
      setSelection({ kind: 'task', rec: emptyTask(ws) })
    } else {
      setSelection({ kind: 'memory', rec: emptyMemory(ws, tab === 'notes' ? 'note' : 'fact') })
    }
  }

  // onSaved refreshes the list + workspace counts, and re-loads the saved
  // record's fresh detail (new on_disk_hash) so a follow-up edit carries the
  // correct CAS token (DESIGN §2 post-save refresh).
  function onSaved(saved: BrainTaskRecord | BrainMemoryRecord) {
    setListKey((k) => k + 1)
    refetchWorkspaces()
    const k = selection?.kind ?? 'task'
    if (saved.id) {
      openRecord(k, saved.id)
    } else {
      setSelection(null)
    }
  }

  const editorInitial = useMemo(() => (selection ? selection.rec : null), [selection])

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold">
            <Brain className="h-6 w-6" /> Brain
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Git-backed knowledge base for the selected workspace. Notes carry context, facts feed recall,
            and tasks track work your agents can act on. Edit directly or let agents curate;
            the index syncs automatically.
          </p>
        </div>
        <Button variant="outline" size="sm" className="rounded-none" onClick={() => refetchWorkspaces()}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" /> Refresh
        </Button>
      </div>

      {loading && !workspaces && <BrainLoadingSkeleton />}
      {error && <BrainErrorCard error={error} />}

      {workspaces && workspaces.length > 0 && (
        <Card className="overflow-hidden rounded-none">
          {/* Scope picker — the spine of the Brain shell (DESIGN §2). */}
          <ScopePicker
            clients={clients ?? []}
            workspaces={workspaces}
            client={client}
            workspace={ws}
            source={source}
            onClient={(c) => {
              setClient(c)
              setSelection(null)
            }}
            onWorkspace={(next) => {
              setWs(next)
              setSelection(null)
            }}
            onSource={(s) => setSource(s)}
          />

          <div className="grid grid-cols-[minmax(320px,420px)_1fr]">
            {/* Record list column */}
            <div className="flex min-h-[60vh] flex-col border-r border-border">
              <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
                <Tabs value={tab} onValueChange={(v) => setTab(v as RecordTab)}>
                  <TabsList variant="line">
                    <TabsTrigger value="notes">Notes</TabsTrigger>
                    <TabsTrigger value="memory">Facts</TabsTrigger>
                    <TabsTrigger value="tasks">Tasks</TabsTrigger>
                  </TabsList>
                </Tabs>
                {tagParam && (
                  <button
                    type="button"
                    onClick={() =>
                      setSearchParams((p) => {
                        p.delete('tag')
                        return p
                      }, { replace: true })
                    }
                    className="flex items-center gap-1 border border-border bg-muted/40 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground hover:text-foreground"
                    aria-label={`Clear tag filter ${tagParam}`}
                  >
                    #{tagParam}
                    <X className="h-3 w-3" />
                  </button>
                )}
              </div>
              <div className="min-h-0 flex-1">
                {listLoading ? (
                  <RecordListSkeleton />
                ) : (
                  <RecordList
                    tasks={tasks}
                    memories={memories}
                    notes={notes}
                    activeTab={tab}
                    statusFilter={statusFilter}
                    selectedId={selection && selection.rec.id ? selection.rec.id : null}
                    onStatusFilter={setStatusFilter}
                    onSelect={openRecord}
                    onNew={startNew}
                  />
                )}
              </div>
              <ScopeFusionLine scope={scope?.scope ?? ''} />
            </div>

            {/* Editor column */}
            <div className="p-4">
              {editorInitial && selection ? (
                <BrainRecordEditor
                  key={selectedId ?? 'new'}
                  kind={selection.kind}
                  initial={editorInitial}
                  onSaved={onSaved}
                  onCancel={() => setSelection(null)}
                />
              ) : (
                <div className="flex h-full min-h-[200px] flex-col items-center justify-center gap-1 text-center text-sm text-muted-foreground">
                  <Brain className="mb-1 h-7 w-7 text-muted-foreground/40" />
                  <span>Pick something on the left to read or edit.</span>
                  <span className="text-xs text-muted-foreground/70">
                    Or start a new one with the + button.
                  </span>
                </div>
              )}
            </div>
          </div>
        </Card>
      )}

      {workspaces && workspaces.length === 0 && <BrainEmptyIndexCard />}
    </div>
  )
}
