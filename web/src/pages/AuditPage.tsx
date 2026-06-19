import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { useAuditStream } from '@/hooks/use-audit-stream'
import { listAuthScopes, listWorkspaces, queryAuditLogs } from '@/api/client'
import type { AuditFilter, AuditRecord } from '@/api/types'
import { Bot, Check, ChevronLeft, ChevronRight, Clock, Filter, Layers, Monitor, Radio, X } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { AuditDetailDialog, ReasonBadge, SecretEventBadge } from '@/components/AuditDetailDialog'
import { useSearchParams } from 'react-router-dom'
import { cn } from '@/lib/utils'
import { classifySecretEvent, normalizeStatus } from '@/lib/audit-semantics'

const PAGE_SIZE = 25

// The gateway stores `model` as the MCP clientInfo "<name>/<version>" hint
// (e.g. "claude-code/1.0.5"), which repeats the harness name already shown in
// `client_type`. For the compact list view, strip that redundant prefix so the
// secondary line reads as just the version/model tail. Returns '' when the
// model adds nothing beyond the harness name.
function harnessModelTail(record: AuditRecord): string {
  const harness = record.client_type ?? ''
  let model = record.model ?? ''
  if (model && harness && model.startsWith(harness + '/')) {
    model = model.slice(harness.length + 1)
  }
  return model === harness ? '' : model
}

export function AuditPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [filter, setFilter] = useState<AuditFilter>(() => {
    const execId = searchParams.get('execution_id')
    const sessId = searchParams.get('session_id')
    const status = searchParams.get('status') as 'success' | 'error' | null
    const wsId = searchParams.get('workspace_id')
    const toolName = searchParams.get('tool_name')
    return {
      limit: PAGE_SIZE,
      offset: 0,
      ...(execId ? { execution_id: execId } : {}),
      ...(sessId ? { session_id: sessId } : {}),
      ...(status === 'success' || status === 'error' ? { status } : {}),
      ...(wsId ? { workspace_id: wsId } : {}),
      ...(toolName ? { tool_name: toolName } : {}),
    }
  })

  // Sync URL search params when filters change. Selection is also URL-backed
  // but written imperatively by `setSelected` so it survives filter changes.
  useEffect(() => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        const setOrDel = (key: string, value: string | undefined) => {
          if (value) next.set(key, value)
          else next.delete(key)
        }
        setOrDel('execution_id', filter.execution_id)
        setOrDel('session_id', filter.session_id)
        setOrDel('status', filter.status)
        setOrDel('workspace_id', filter.workspace_id)
        setOrDel('tool_name', filter.tool_name)
        return next
      },
      { replace: true },
    )
  }, [
    filter.execution_id,
    filter.session_id,
    filter.status,
    filter.workspace_id,
    filter.tool_name,
    setSearchParams,
  ])

  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)

  const authScopesFetcher = useCallback(() => listAuthScopes(), [])
  const { data: authScopes } = useApi(authScopesFetcher)

  const wsName = (id: string) => workspaces?.find((w) => w.id === id)?.name ?? id
  const asName = (id: string) => authScopes?.find((a) => a.id === id)?.name ?? id

  // Live stream (always connected)
  const streamFilter = useMemo(
    () => ({
      workspace_id: filter.workspace_id,
      tool_name: filter.tool_name,
      status: filter.status,
      execution_id: filter.execution_id,
      session_id: filter.session_id,
    }),
    [filter.workspace_id, filter.tool_name, filter.status, filter.execution_id, filter.session_id],
  )
  const { records: liveRecords, connected, clear } = useAuditStream(streamFilter)

  // History (paginated)
  const historyFetcher = useCallback(() => queryAuditLogs(filter), [filter])
  const { data: historyData, loading, error } = useApi(historyFetcher)

  const page = Math.floor((filter.offset ?? 0) / PAGE_SIZE) + 1
  const totalPages = historyData ? Math.ceil(historyData.total / PAGE_SIZE) : 0
  const isFirstPage = page === 1

  // On page 1, show live events (deduped) then history. Other pages: just history.
  const historyRecords = useMemo(() => historyData?.data ?? [], [historyData])
  const uniqueLive = useMemo(() => {
    if (!isFirstPage) return []
    const historyIds = new Set(historyRecords.map((r) => r.id))
    return liveRecords.filter((r) => !historyIds.has(r.id))
  }, [historyRecords, liveRecords, isFirstPage])
  const allRecords = useMemo(
    () => [...uniqueLive, ...historyRecords],
    [uniqueLive, historyRecords],
  )

  // Selection is URL-backed (?selected=<id>). Derived rather than stored so
  // deep-links and refreshes pick up automatically once allRecords arrive.
  //
  // UI-5 fix — if the record isn't in the current paginated view (deep
  // link from dashboard / signal-tray hitting a record on an old page
  // or filtered out by the current filter), fetch it by id directly
  // and pin the drawer open. Without this the drawer never opens and
  // the click feels broken.
  const selectedId = searchParams.get('selected')
  const [fallbackRecord, setFallbackRecord] = useState<AuditRecord | null>(null)
  useEffect(() => {
    if (!selectedId) {
      setFallbackRecord(null)
      return
    }
    if (allRecords.some((r) => r.id === selectedId)) {
      setFallbackRecord(null)
      return
    }
    let cancelled = false
    queryAuditLogs({ id: selectedId, limit: 1 })
      .then((resp) => {
        if (cancelled) return
        setFallbackRecord(resp.data?.[0] ?? null)
      })
      .catch(() => {
        if (!cancelled) setFallbackRecord(null)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId, allRecords])
  const selected = useMemo<AuditRecord | null>(
    () =>
      selectedId
        ? allRecords.find((r) => r.id === selectedId) ?? fallbackRecord
        : null,
    [selectedId, allRecords, fallbackRecord],
  )
  const setSelected = useCallback(
    (record: AuditRecord | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (record) next.set('selected', record.id)
          else next.delete('selected')
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const selectedIndex = selected
    ? allRecords.findIndex((r) => r.id === selected.id)
    : -1
  const hasPrev = selectedIndex > 0
  const hasNext = selectedIndex >= 0 && selectedIndex < allRecords.length - 1
  const goPrev = useCallback(() => {
    if (selectedIndex > 0) setSelected(allRecords[selectedIndex - 1])
  }, [selectedIndex, allRecords, setSelected])
  const goNext = useCallback(() => {
    if (selectedIndex >= 0 && selectedIndex < allRecords.length - 1) {
      setSelected(allRecords[selectedIndex + 1])
    }
  }, [selectedIndex, allRecords, setSelected])

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">Audit Logs</h1>
        <div className="flex items-center gap-3">
          {uniqueLive.length > 0 && (
            <Button variant="ghost" size="sm" onClick={clear} data-testid="audit-clear-live">
              Clear live
            </Button>
          )}
          <div className="flex items-center gap-2 text-sm">
            {connected ? (
              <>
                <span className="relative flex h-2.5 w-2.5">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                  <span className="relative inline-flex h-2.5 w-2.5 rounded-full bg-emerald-500" />
                </span>
                <span className="text-emerald-400">Live</span>
              </>
            ) : (
              <>
                <span className="h-2.5 w-2.5 rounded-full bg-muted-foreground/40" />
                <span className="text-muted-foreground">Connecting...</span>
              </>
            )}
          </div>
        </div>
      </div>

      {(filter.execution_id || filter.session_id) && (
        <Card>
          <CardContent className="flex flex-wrap items-center gap-3 pt-6">
            {filter.session_id && (
              <>
                <Monitor className="h-4 w-4 text-cyan-400" />
                <span className="text-sm">
                  Session{' '}
                  <code className="rounded bg-cyan-500/10 px-1.5 py-0.5 font-mono text-xs text-cyan-400">
                    {filter.session_id.slice(0, 8)}
                  </code>
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 gap-1 text-xs"
                  aria-label="Clear session filter"
                  data-testid="audit-clear-session"
                  onClick={() => setFilter((f) => ({ ...f, session_id: undefined, offset: 0 }))}
                >
                  <X className="h-3 w-3" />
                </Button>
              </>
            )}
            {filter.execution_id && (
              <>
                <Layers className="h-4 w-4 text-violet-400" />
                <span className="text-sm">
                  Execution{' '}
                  <code className="rounded bg-violet-500/10 px-1.5 py-0.5 font-mono text-xs text-violet-400">
                    {filter.execution_id.slice(0, 8)}
                  </code>
                </span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 gap-1 text-xs"
                  aria-label="Clear execution filter"
                  data-testid="audit-clear-execution"
                  onClick={() => setFilter((f) => ({ ...f, execution_id: undefined, offset: 0 }))}
                >
                  <X className="h-3 w-3" />
                </Button>
              </>
            )}
            <Button
              variant="ghost"
              size="sm"
              className="ml-auto h-7 gap-1 text-xs"
              data-testid="audit-clear-all"
              onClick={() => setFilter((f) => ({ ...f, execution_id: undefined, session_id: undefined, offset: 0 }))}
            >
              Clear all
            </Button>
          </CardContent>
        </Card>
      )}

      <FilterBar filter={filter} setFilter={setFilter} workspaces={workspaces ?? []} />

      <Card>
        <CardContent className="pt-6">
          {loading && !historyData && (
            <div className="flex items-center gap-2 text-muted-foreground">
              <div className="h-2 w-2 rounded-full bg-primary/60" />
              Loading...
            </div>
          )}
          {error && <p className="text-destructive">Error: {error}</p>}

          <Table className="table-fixed">
            <colgroup>
              <col className="w-[7rem]" />
              <col className="w-[18rem]" />
              <col className="hidden md:table-column w-[10rem]" />
              <col className="hidden lg:table-column w-[8rem]" />
              <col className="hidden lg:table-column w-[9rem]" />
              <col className="w-[6rem]" />
              <col />
              <col className="hidden lg:table-column w-[5rem]" />
              <col className="hidden lg:table-column w-[8rem]" />
              <col className="hidden sm:table-column w-[5rem]" />
            </colgroup>
            <TableHeader>
              <TableRow className="border-border/50 hover:bg-transparent">
                <TableHead>Timestamp</TableHead>
                <TableHead>Tool</TableHead>
                <TableHead className="hidden md:table-cell">Workspace</TableHead>
                <TableHead className="hidden lg:table-cell">Session</TableHead>
                <TableHead className="hidden lg:table-cell">Client</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead className="hidden lg:table-cell">Cache</TableHead>
                <TableHead className="hidden lg:table-cell">Group</TableHead>
                <TableHead className="hidden sm:table-cell text-right">Latency</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {allRecords.length === 0 && !loading ? (
                <TableRow>
                  <TableCell colSpan={10} className="h-32">
                    <div className="flex flex-col items-center justify-center text-muted-foreground">
                      <Radio className="mb-2 h-8 w-8 text-muted-foreground/50" />
                      <p className="text-sm">Waiting for events...</p>
                      <p className="text-xs text-muted-foreground/60">
                        New audit records will appear here in real-time
                      </p>
                    </div>
                  </TableCell>
                </TableRow>
              ) : (
                allRecords.map((record, idx) => {
                  const isLive = idx < uniqueLive.length
                  return (
                    <TableRow
                      key={record.id}
                      tabIndex={0}
                      aria-label={`View audit record for ${record.tool_name}`}
                      className={`cursor-pointer border-border/30 hover:bg-muted/30 focus-visible:bg-muted/30 focus-visible:outline-none ${
                        isLive && idx === 0 ? 'animate-[audit-in_0.3s_ease-out]' : ''
                      }`}
                      onClick={() => setSelected(record)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          setSelected(record)
                        }
                      }}
                    >
                      <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <span>{new Date(record.timestamp).toLocaleTimeString()}</span>
                          </TooltipTrigger>
                          <TooltipContent>{new Date(record.timestamp).toLocaleString()}</TooltipContent>
                        </Tooltip>
                      </TableCell>
                      <TableCell>
                        <div className="max-w-[20rem] truncate font-mono text-sm text-accent-foreground" title={record.tool_name}>
                          {record.tool_name}
                        </div>
                        {classifySecretEvent(record.tool_name) && (
                          <div className="mt-1 flex items-center gap-1.5">
                            <SecretEventBadge toolName={record.tool_name} />
                            {record.auth_scope_id && (
                              <span className="truncate text-[11px] text-muted-foreground" title={asName(record.auth_scope_id)}>
                                {asName(record.auth_scope_id)}
                              </span>
                            )}
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="hidden md:table-cell text-muted-foreground">
                        <div className="max-w-[10rem] truncate">{record.workspace_name || (record.workspace_id ? wsName(record.workspace_id) : '-')}</div>
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        {record.session_id && (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge
                                variant="outline"
                                className="cursor-pointer border-cyan-500/40 text-cyan-400 hover:bg-cyan-500/10"
                                onClick={(e) => {
                                  e.stopPropagation()
                                  setFilter((f) => ({
                                    ...f,
                                    session_id: record.session_id,
                                    offset: 0,
                                  }))
                                }}
                              >
                                <Monitor className="mr-1 h-3 w-3" />
                                {record.session_id.slice(0, 8)}
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>View all calls in this session</TooltipContent>
                          </Tooltip>
                        )}
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        {record.client_type ? (
                          <div className="min-w-0">
                            <div
                              className="flex items-center gap-1 text-xs text-foreground"
                              title={record.client_type}
                            >
                              <Bot className="h-3 w-3 shrink-0 opacity-60" />
                              <span className="truncate">{record.client_type}</span>
                            </div>
                            {harnessModelTail(record) && (
                              <div
                                className="mt-0.5 truncate pl-4 font-mono text-[10px] text-muted-foreground/70"
                                title={record.model}
                              >
                                {harnessModelTail(record)}
                              </div>
                            )}
                          </div>
                        ) : (
                          <span className="text-muted-foreground/40">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {(() => {
                          const tone = normalizeStatus(record.status)
                          return (
                            <Badge
                              variant={tone === 'success' ? 'secondary' : tone === 'blocked' ? 'outline' : 'destructive'}
                              className={tone === 'blocked' ? 'border-amber-500/40 text-amber-500' : ''}
                            >
                              {record.status}
                            </Badge>
                          )
                        })()}
                      </TableCell>
                      <TableCell className="align-top">
                        <ReasonBadge record={record} />
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        {record.cache_hit && (
                          <Badge variant="outline" className="border-blue-500/40 text-blue-400">
                            cached
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        {record.execution_id && (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge
                                variant="outline"
                                className="cursor-pointer border-violet-500/40 text-violet-400 hover:bg-violet-500/10"
                                onClick={(e) => {
                                  e.stopPropagation()
                                  setFilter((f) => ({
                                    ...f,
                                    execution_id: record.execution_id,
                                    offset: 0,
                                  }))
                                }}
                              >
                                <Layers className="mr-1 h-3 w-3" />
                                {record.execution_id.slice(0, 8)}
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>View all calls in this execution</TooltipContent>
                          </Tooltip>
                        )}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell text-right font-mono text-sm text-muted-foreground">
                        {record.latency_ms}ms
                      </TableCell>
                    </TableRow>
                  )
                })
              )}
            </TableBody>
          </Table>

          {historyData && (
            <div className="mt-4 flex items-center justify-between">
              <p className="text-sm text-muted-foreground">
                {historyData.total} total records
              </p>
              <div className="flex items-center gap-1">
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-8 w-8 p-0"
                      aria-label="Previous page"
                      data-testid="audit-page-prev"
                      disabled={page <= 1}
                      onClick={() =>
                        setFilter((f) => ({
                          ...f,
                          offset: Math.max(0, (f.offset ?? 0) - PAGE_SIZE),
                        }))
                      }
                    >
                      <ChevronLeft className="h-4 w-4" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Previous page</TooltipContent>
                </Tooltip>
                <PageJump
                  page={page}
                  total={Math.max(1, totalPages)}
                  onJump={(p) =>
                    setFilter((f) => ({
                      ...f,
                      offset: Math.max(0, (p - 1) * PAGE_SIZE),
                    }))
                  }
                />
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-8 w-8 p-0"
                      aria-label="Next page"
                      data-testid="audit-page-next"
                      disabled={page >= totalPages}
                      onClick={() =>
                        setFilter((f) => ({
                          ...f,
                          offset: (f.offset ?? 0) + PAGE_SIZE,
                        }))
                      }
                    >
                      <ChevronRight className="h-4 w-4" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Next page</TooltipContent>
                </Tooltip>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <AuditDetailDialog
        record={selected}
        onClose={() => setSelected(null)}
        wsName={wsName}
        asName={asName}
        onPrev={goPrev}
        onNext={goNext}
        hasPrev={hasPrev}
        hasNext={hasNext}
      />
    </div>
  )
}

// PageJump renders the "page / total" pill as a jump-to-page input. The
// pill behaviour is preserved (mono, tight, tabular nums); clicking
// enters edit mode where the user types a target page and hits Enter.
// Esc reverts; blur commits. Way faster than chevron-clicking through
// 47 pages.
function PageJump({
  page,
  total,
  onJump,
}: {
  page: number
  total: number
  onJump: (p: number) => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(String(page))
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (!editing) setDraft(String(page))
  }, [page, editing])

  useEffect(() => {
    if (editing) inputRef.current?.select()
  }, [editing])

  function commit() {
    const n = parseInt(draft, 10)
    if (Number.isFinite(n)) {
      const clamped = Math.max(1, Math.min(total, n))
      if (clamped !== page) onJump(clamped)
    }
    setEditing(false)
  }

  if (!editing) {
    return (
      <button
        type="button"
        onClick={() => setEditing(true)}
        data-testid="audit-page-jump"
        className="bg-secondary px-3 py-1 font-mono text-xs font-medium tabular-nums transition-colors hover:bg-secondary/80"
        title="Jump to page"
        aria-label={`Page ${page} of ${total}. Click to jump.`}
      >
        {page} / {total}
      </button>
    )
  }

  return (
    <span className="inline-flex items-center gap-1 bg-secondary px-2 py-1 font-mono text-xs tabular-nums">
      <input
        ref={inputRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value.replace(/[^0-9]/g, ''))}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            commit()
          } else if (e.key === 'Escape') {
            e.preventDefault()
            setEditing(false)
            setDraft(String(page))
          }
        }}
        onBlur={commit}
        inputMode="numeric"
        data-testid="audit-page-jump-input"
        aria-label="Page number"
        className="w-10 border-0 bg-transparent p-0 text-center font-mono text-xs tabular-nums outline-none focus:outline-none focus-visible:ring-0 focus-visible:ring-offset-0"
      />
      <span className="text-muted-foreground">/ {total}</span>
    </span>
  )
}

function FilterBar({
  filter,
  setFilter,
  workspaces,
}: {
  filter: AuditFilter
  setFilter: React.Dispatch<React.SetStateAction<AuditFilter>>
  workspaces: { id: string; name: string }[]
}) {
  const wsName = filter.workspace_id
    ? workspaces.find((w) => w.id === filter.workspace_id)?.name ?? filter.workspace_id
    : null

  const setF = (patch: Partial<AuditFilter>) => setFilter((f) => ({ ...f, ...patch, offset: 0 }))

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="mr-1 inline-flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
        <Filter className="h-3 w-3" />
        Filters
      </span>

      <WorkspaceChip
        workspaces={workspaces}
        value={filter.workspace_id ?? null}
        label={wsName}
        onSet={(id) => setF({ workspace_id: id ?? undefined })}
      />

      <StatusChip
        value={filter.status ?? null}
        onSet={(v) => setF({ status: v ?? undefined })}
      />

      <ToolChip
        value={filter.tool_name ?? null}
        onSet={(v) => setF({ tool_name: v ?? undefined })}
      />

      <TimeChip
        after={filter.after ?? null}
        before={filter.before ?? null}
        onSet={(after, before) => setF({ after: after ?? undefined, before: before ?? undefined })}
      />

      {(filter.workspace_id || filter.status || filter.tool_name || filter.after || filter.before) && (
        <button
          type="button"
          onClick={() =>
            setF({
              workspace_id: undefined,
              status: undefined,
              tool_name: undefined,
              after: undefined,
              before: undefined,
            })
          }
          data-testid="audit-filter-clear-all"
          className="ml-1 text-[11px] text-muted-foreground hover:text-foreground"
        >
          Clear filters
        </button>
      )}
    </div>
  )
}

function ChipShell({
  active,
  testId,
  children,
  onClear,
}: {
  active: boolean
  testId?: string
  children: React.ReactNode
  onClear?: () => void
}) {
  return (
    <span
      data-testid={testId}
      className={cn(
        'inline-flex items-center gap-1 border px-2 py-1 text-[12px] font-mono transition-colors',
        active
          ? 'border-primary/40 bg-primary/5 text-foreground'
          : 'border-dashed border-border text-muted-foreground hover:border-border/80 hover:text-foreground',
      )}
    >
      {children}
      {active && onClear && (
        <button
          type="button"
          aria-label="Clear filter"
          onClick={(e) => {
            e.stopPropagation()
            onClear()
          }}
          className="ml-0.5 -mr-0.5 text-muted-foreground hover:text-foreground"
        >
          <X className="h-3 w-3" />
        </button>
      )}
    </span>
  )
}

function WorkspaceChip({
  workspaces,
  value,
  label,
  onSet,
}: {
  workspaces: { id: string; name: string }[]
  value: string | null
  label: string | null
  onSet: (id: string | null) => void
}) {
  const active = value !== null
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button">
          <ChipShell
            active={active}
            testId="audit-filter-workspace"
            onClear={() => onSet(null)}
          >
            {active ? <>in <span className="text-primary">{label}</span></> : '+ Workspace'}
          </ChipShell>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[12rem]">
        <DropdownMenuItem onClick={() => onSet(null)}>
          <span className="text-muted-foreground">All workspaces</span>
        </DropdownMenuItem>
        {workspaces.map((w) => (
          <DropdownMenuItem key={w.id} onClick={() => onSet(w.id)}>
            <Check className={cn('mr-2 h-3 w-3', value === w.id ? 'opacity-100' : 'opacity-0')} />
            {w.name}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

type AuditStatus = 'success' | 'error' | 'blocked'

function StatusChip({
  value,
  onSet,
}: {
  value: AuditStatus | null
  onSet: (v: AuditStatus | null) => void
}) {
  const active = value !== null
  const options: AuditStatus[] = ['success', 'error', 'blocked']
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button">
          <ChipShell active={active} testId="audit-filter-status" onClear={() => onSet(null)}>
            {active ? <>status <span className="text-primary">{value}</span></> : '+ Status'}
          </ChipShell>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {options.map((opt) => (
          <DropdownMenuItem key={opt} onClick={() => onSet(opt)}>
            <Check className={cn('mr-2 h-3 w-3', value === opt ? 'opacity-100' : 'opacity-0')} />
            <span className="capitalize">{opt}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ToolChip({
  value,
  onSet,
}: {
  value: string | null
  onSet: (v: string | null) => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const inputRef = useRef<HTMLInputElement | null>(null)
  const active = value !== null

  useEffect(() => {
    if (editing) inputRef.current?.focus()
  }, [editing])

  if (editing) {
    return (
      <span className="inline-flex items-center gap-1 border border-primary/40 bg-primary/5 px-2 py-1 font-mono text-[12px]">
        <span className="text-muted-foreground">tool =</span>
        <Input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              onSet(draft.trim() || null)
              setEditing(false)
            } else if (e.key === 'Escape') {
              setEditing(false)
              setDraft('')
            }
          }}
          onBlur={() => {
            onSet(draft.trim() || null)
            setEditing(false)
          }}
          className="h-5 w-32 border-0 bg-transparent px-0 py-0 font-mono text-[12px] focus-visible:ring-0"
          data-testid="audit-filter-tool-input"
          aria-label="Filter by tool name"
          placeholder="name…"
        />
      </span>
    )
  }

  return (
    <button
      type="button"
      onClick={() => {
        setDraft(value ?? '')
        setEditing(true)
      }}
    >
      <ChipShell active={active} testId="audit-filter-tool" onClear={() => onSet(null)}>
        {active ? <>tool <span className="text-primary">{value}</span></> : '+ Tool name'}
      </ChipShell>
    </button>
  )
}

function TimeChip({
  after,
  before,
  onSet,
}: {
  after: string | null
  before: string | null
  onSet: (after: string | null, before: string | null) => void
}) {
  const active = after !== null || before !== null
  const [customOpen, setCustomOpen] = useState(false)

  function applyPreset(hoursBack: number | null) {
    if (hoursBack === null) {
      onSet(null, null)
      return
    }
    const now = new Date()
    const past = new Date(now.getTime() - hoursBack * 3600_000)
    onSet(toLocal(past), null)
  }

  function toLocal(d: Date): string {
    // datetime-local string: YYYY-MM-DDTHH:MM
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
  }

  function label(): string {
    if (!active) return '+ Time'
    if (after && !before) return `since ${shortTime(after)}`
    if (!after && before) return `until ${shortTime(before)}`
    return `${shortTime(after!)} → ${shortTime(before!)}`
  }

  function shortTime(s: string): string {
    try {
      const d = new Date(s)
      return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    } catch {
      return s
    }
  }

  if (customOpen) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1.5 border border-primary/40 bg-primary/5 px-2 py-1 font-mono text-[12px]">
        <span className="text-muted-foreground">time:</span>
        <Input
          type="datetime-local"
          value={after ?? ''}
          onChange={(e) => onSet(e.target.value || null, before)}
          className="h-6 w-44 border-0 bg-transparent px-0 py-0 font-mono text-[11px] focus-visible:ring-0"
          data-testid="audit-filter-after"
          aria-label="Filter from time"
        />
        <span className="text-muted-foreground">→</span>
        <Input
          type="datetime-local"
          value={before ?? ''}
          onChange={(e) => onSet(after, e.target.value || null)}
          className="h-6 w-44 border-0 bg-transparent px-0 py-0 font-mono text-[11px] focus-visible:ring-0"
          data-testid="audit-filter-before"
          aria-label="Filter to time"
        />
        <button
          type="button"
          onClick={() => setCustomOpen(false)}
          className="text-muted-foreground hover:text-foreground"
          aria-label="Close time picker"
        >
          <X className="h-3 w-3" />
        </button>
      </span>
    )
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button">
          <ChipShell active={active} testId="audit-filter-time" onClear={() => onSet(null, null)}>
            <Clock className="h-3 w-3 opacity-60" />
            {label()}
          </ChipShell>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[10rem]">
        <DropdownMenuItem onClick={() => applyPreset(1)}>Last hour</DropdownMenuItem>
        <DropdownMenuItem onClick={() => applyPreset(24)}>Last 24h</DropdownMenuItem>
        <DropdownMenuItem onClick={() => applyPreset(24 * 7)}>Last 7d</DropdownMenuItem>
        <DropdownMenuItem onClick={() => applyPreset(null)}>All time</DropdownMenuItem>
        <DropdownMenuItem onClick={() => setCustomOpen(true)}>Custom…</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
