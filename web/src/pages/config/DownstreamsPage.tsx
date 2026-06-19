import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import {
  createDownstream,
  deleteDownstream,
  fetchCatalog,
  getDownstreamOAuthStatus,
  listAuthScopes,
  listDownstreams,
  listRoutes,
  updateDownstream,
} from '@/api/client'
import type { AuthScope, DownstreamOAuthStatusEntry, DownstreamServer } from '@/api/types'
import { CATEGORY_LABELS, CATEGORY_ORDER, toCatalogEntry } from '@/data/server-catalog'
import type { CatalogEntry, ServerCategory } from '@/data/server-catalog'
import { ServerCardGrid } from './ServerCardGrid'
import type { MergedServer } from './ServerCardGrid'
import { ServerCatalogList } from './ServerCatalogList'
import { ConnectDialog } from './ConnectDialog'
import { DownstreamDialog, emptyDownstreamForm } from './DownstreamDialog'
import type { DownstreamFormData } from './DownstreamDialog'
import { ApiKeyDialog } from './ApiKeyDialog'
import { HammerspoonPanel } from './HammerspoonPanel'
import { Plus, Search, Server } from 'lucide-react'
import { cn } from '@/lib/utils'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState as SharedEmptyState } from '@/components/ui/empty-state'

const INTERNAL_IDS = new Set(['mcplexer', 'mcpx-builtin', 'mesh-builtin'])

export type DownstreamsMode = 'installed' | 'available'

// A row is "user-installed" iff it has a DB entry that isn't a seeded-but-untouched default.
// Seed policy (internal/config/seed_servers.go): external catalog entries seed disabled=true,
// source="default". Once the user adds or enables one, either disabled flips false or source
// is empty (user-created via API). Anything else is just catalog metadata in the DB.
function isUserInstalled(db: DownstreamServer | null | undefined): boolean {
  if (!db) return false
  if (db.source === 'default' && db.disabled) return false
  return true
}

interface DownstreamsPageProps {
  mode?: DownstreamsMode
  embedded?: boolean
}

export function DownstreamsPage({ mode = 'installed', embedded = false }: DownstreamsPageProps) {
  const fetcher = useCallback(() => listDownstreams(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<DownstreamServer | null>(null)
  const [form, setForm] = useState<DownstreamFormData>(emptyDownstreamForm)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [connectDialogOpen, setConnectDialogOpen] = useState(false)
  const [connectServer, setConnectServer] = useState<DownstreamServer | null>(null)
  const [oauthStatuses, setOauthStatuses] = useState<Record<string, DownstreamOAuthStatusEntry[]>>({})
  const [statusErrors, setStatusErrors] = useState<Record<string, boolean>>({})
  const [deleteTarget, setDeleteTarget] = useState<DownstreamServer | null>(null)
  const [apiKeyTarget, setApiKeyTarget] = useState<{ server: DownstreamServer; scope: AuthScope } | null>(null)
  const [hammerspoonTarget, setHammerspoonTarget] = useState<DownstreamServer | null>(null)
  const [serverAuthScopes, setServerAuthScopes] = useState<Record<string, AuthScope>>({})
  const [catalogEntries, setCatalogEntries] = useState<CatalogEntry[]>([])

  // Fetch server catalog from API
  useEffect(() => {
    fetchCatalog()
      .then((res) => setCatalogEntries(res.entries.map(toCatalogEntry)))
      .catch(() => {})
  }, [])

  // Filters
  const [search, setSearch] = useState('')
  const [categoryFilter, setCategoryFilter] = useState<ServerCategory | 'all'>('all')

  // Reset filters when switching tabs so a stale category doesn't hide everything.
  useEffect(() => {
    setSearch('')
    setCategoryFilter('all')
  }, [mode])

  // Legacy deep links with ?server=<name> auto-fill the search box so the
  // operator lands on the row they wanted to act on. We strip the param
  // afterwards so a browser back/refresh doesn't re-pin it.
  const [searchParams, setSearchParams] = useSearchParams()
  useEffect(() => {
    const target = searchParams.get('server')
    if (!target) return
    setSearch(target)
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        next.delete('server')
        return next
      },
      { replace: true },
    )
  }, [searchParams, setSearchParams])

  // Fetch auth scopes
  useEffect(() => {
    let active = true
    Promise.all([listRoutes(), listAuthScopes()]).then(([routes, scopes]) => {
      if (!active) return
      const scopeById = new Map(scopes.filter((s) => s.type === 'env' || s.type === 'client_credentials').map((s) => [s.id, s]))
      const result: Record<string, AuthScope> = {}
      for (const rule of routes) {
        if (rule.auth_scope_id && scopeById.has(rule.auth_scope_id) && rule.downstream_server_id) {
          result[rule.downstream_server_id] = scopeById.get(rule.auth_scope_id)!
        }
      }
      setServerAuthScopes(result)
    }).catch(() => {})
    return () => { active = false }
  }, [data])

  // Fetch OAuth statuses
  useEffect(() => {
    if (!data) return
    let active = true
    for (const ds of data) {
      if (ds.transport === 'http') {
        getDownstreamOAuthStatus(ds.id)
          .then((res) => {
            if (!active) return
            setOauthStatuses((prev) => ({ ...prev, [ds.id]: res.entries }))
          })
          .catch(() => {
            if (!active) return
            setStatusErrors((prev) => ({ ...prev, [ds.id]: true }))
          })
      }
    }
    return () => { active = false }
  }, [data])

  // Merge catalog with DB
  const mergedServers = useMemo<MergedServer[]>(() => {
    const dbById = new Map((data ?? []).filter((ds) => !INTERNAL_IDS.has(ds.id)).map((ds) => [ds.id, ds]))
    const merged: MergedServer[] = catalogEntries.map((entry) => ({
      catalog: entry,
      db: dbById.get(entry.id) ?? null,
    }))
    const catalogIds = new Set(catalogEntries.map((e) => e.id))
    for (const ds of data ?? []) {
      if (!catalogIds.has(ds.id) && !INTERNAL_IDS.has(ds.id)) {
        merged.push({ catalog: null, db: ds })
      }
    }
    return merged
  }, [data, catalogEntries])

  // Mode-aware filter
  const visibleByMode = useMemo(() => {
    return mergedServers.filter((s) =>
      mode === 'installed' ? isUserInstalled(s.db) : !isUserInstalled(s.db),
    )
  }, [mergedServers, mode])

  // Search + category filter applied on top
  const filtered = useMemo(() => {
    let result = visibleByMode

    if (categoryFilter !== 'all') {
      result = result.filter((s) => s.catalog?.category === categoryFilter)
    }

    if (search.trim()) {
      const q = search.toLowerCase()
      result = result.filter((s) => {
        const name = (s.catalog?.name ?? s.db?.name ?? '').toLowerCase()
        const desc = (s.catalog?.description ?? '').toLowerCase()
        const tags = (s.catalog?.tags ?? []).join(' ').toLowerCase()
        const ns = (s.catalog?.preset.tool_namespace ?? s.db?.tool_namespace ?? '').toLowerCase()
        return name.includes(q) || desc.includes(q) || tags.includes(q) || ns.includes(q)
      })
    }

    return result
  }, [visibleByMode, search, categoryFilter])

  // Counts for the tab badges
  const installedCount = useMemo(
    () => mergedServers.filter((s) => isUserInstalled(s.db)).length,
    [mergedServers],
  )
  const availableCount = useMemo(
    () => mergedServers.filter((s) => !isUserInstalled(s.db)).length,
    [mergedServers],
  )

  // Actions
  function openCreate() {
    setEditing(null)
    setForm(emptyDownstreamForm)
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleAdd(catalog: CatalogEntry) {
    try {
      await createDownstream({
        id: catalog.id,
        name: catalog.name,
        transport: catalog.preset.transport,
        command: catalog.preset.command ?? '',
        args: catalog.preset.args ?? [],
        url: catalog.preset.url ?? null,
        tool_namespace: catalog.preset.tool_namespace,
        idle_timeout_sec: 300,
        max_instances: 1,
        restart_policy: 'on-failure',
        disabled: false,
      })
      toast.success(`${catalog.name} installed`)
      refetch()
      Promise.all([listRoutes(), listAuthScopes()]).then(([routes, scopes]) => {
        const scopeById = new Map(
          scopes.filter((s) => s.type === 'env' || s.type === 'client_credentials').map((s) => [s.id, s]),
        )
        const result: Record<string, AuthScope> = {}
        for (const rule of routes) {
          if (rule.auth_scope_id && scopeById.has(rule.auth_scope_id) && rule.downstream_server_id) {
            result[rule.downstream_server_id] = scopeById.get(rule.auth_scope_id)!
          }
        }
        setServerAuthScopes(result)
      }).catch(() => {})
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : `Failed to install ${catalog.name}`)
    }
  }

  async function handleEnable(ds: DownstreamServer) {
    try {
      await updateDownstream(ds.id, { disabled: false })
      toast.success(`${ds.name} enabled`)
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to enable server')
    }
  }

  function handleDuplicate(ds: DownstreamServer) {
    setEditing(null)
    setForm({
      name: `${ds.name} (copy)`,
      transport: ds.transport,
      command: ds.command,
      args: [...(ds.args || [])],
      url: ds.url,
      tool_namespace: `${ds.tool_namespace}_copy`,
      idle_timeout_sec: ds.idle_timeout_sec,
      max_instances: ds.max_instances,
      restart_policy: ds.restart_policy,
      disabled: false,
      cache_config: ds.cache_config ? { ...ds.cache_config } : undefined,
    })
    setSaveError(null)
    setDialogOpen(true)
  }

  function openEdit(ds: DownstreamServer) {
    setEditing(ds)
    setForm({
      name: ds.name,
      transport: ds.transport,
      command: ds.command,
      args: [...(ds.args || [])],
      url: ds.url,
      tool_namespace: ds.tool_namespace,
      idle_timeout_sec: ds.idle_timeout_sec,
      max_instances: ds.max_instances,
      restart_policy: ds.restart_policy,
      disabled: ds.disabled,
      cache_config: ds.cache_config ? { ...ds.cache_config } : undefined,
    })
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleSave() {
    setSaving(true)
    setSaveError(null)
    try {
      if (editing) {
        await updateDownstream(editing.id, form)
      } else {
        await createDownstream(form)
      }
      setDialogOpen(false)
      toast.success(editing ? 'Server updated' : 'Server created')
      refetch()
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save downstream server')
    } finally {
      setSaving(false)
    }
  }

  function openConnect(ds: DownstreamServer) {
    setConnectServer(ds)
    setConnectDialogOpen(true)
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    try {
      await deleteDownstream(deleteTarget.id)
      setDeleteTarget(null)
      toast.success('Server deleted')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete downstream server')
    }
  }

  const showCategoryFilter = mode === 'available' || installedCount > 6

  return (
    <div className="space-y-5">
      {/* Header */}
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <p className="text-sm text-muted-foreground">
          {mode === 'installed'
            ? 'MCP tool servers you have installed and can route through MCPlexer.'
            : 'Browse the catalog and install one with a single click.'}
        </p>
        {mode === 'installed' && (
          <Button onClick={openCreate} data-testid="downstream-add-custom">
            <Plus className="mr-2 h-4 w-4" />
            Add Custom
          </Button>
        )}
      </div>

      {/* Tab nav (router-driven) */}
      <ModeTabs current={mode} installedCount={installedCount} availableCount={availableCount} embedded={embedded} />

      {/* Search */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="relative max-w-sm flex-1">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={mode === 'installed' ? 'Search installed servers...' : 'Search catalog...'}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
            data-testid="downstream-search"
            aria-label="Search servers"
          />
        </div>
      </div>

      {/* Category filter chips — useful on Available; also on Installed once it's busy */}
      {showCategoryFilter && (
        <div className="flex flex-wrap gap-1.5">
          <Button
            variant={categoryFilter === 'all' ? 'default' : 'outline'}
            size="sm"
            className="h-7 text-xs"
            onClick={() => setCategoryFilter('all')}
          >
            All
          </Button>
          {CATEGORY_ORDER.map((cat) => (
            <Button
              key={cat}
              variant={categoryFilter === cat ? 'default' : 'outline'}
              size="sm"
              className="h-7 text-xs"
              onClick={() => setCategoryFilter(categoryFilter === cat ? 'all' : cat)}
            >
              {CATEGORY_LABELS[cat]}
            </Button>
          ))}
        </div>
      )}

      {/* Loading / Error */}
      {loading && !data && (
        <div className="flex items-center gap-2 text-muted-foreground">
          <div className="h-2 w-2 rounded-full bg-primary/60" />
          Loading...
        </div>
      )}
      {error && <p className="text-destructive">Error: {error}</p>}

      {/* Available = browse catalog (vertical list, category-grouped, 25-30
          rows visible). Installed = card grid with full per-server state. */}
      {data && filtered.length === 0 && !search && categoryFilter === 'all' ? (
        <EmptyState mode={mode} />
      ) : (
        data && (
          mode === 'available' ? (
            <ServerCatalogList servers={filtered} onAdd={handleAdd} />
          ) : (
            <ServerCardGrid
              servers={filtered}
              oauthStatuses={oauthStatuses}
              statusErrors={statusErrors}
              serverAuthScopes={serverAuthScopes}
              onAdd={handleAdd}
              onEnable={handleEnable}
              onEdit={openEdit}
              onDuplicate={handleDuplicate}
              onDelete={setDeleteTarget}
              onConnect={openConnect}
              onApiKey={(ds, scope) => setApiKeyTarget({ server: ds, scope })}
              onOpenHammerspoon={setHammerspoonTarget}
            />
          )
        )
      )}

      {/* Dialogs */}
      <DownstreamDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={handleSave}
        saving={saving}
        editing={!!editing}
        saveError={saveError}
      />

      <ConnectDialog
        open={connectDialogOpen}
        onClose={() => setConnectDialogOpen(false)}
        server={connectServer}
        onConnected={refetch}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Delete downstream server"
        description={`Are you sure you want to delete "${deleteTarget?.name}"?`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={confirmDelete}
      />

      <HammerspoonPanel
        open={!!hammerspoonTarget}
        onClose={() => {
          setHammerspoonTarget(null)
          // Re-fetch downstreams so the cached CapabilitiesCache.health
          // displayed on the card reflects any probe ran from inside the panel.
          refetch()
        }}
        server={hammerspoonTarget}
      />

      {apiKeyTarget && (
        <ApiKeyDialog
          open={!!apiKeyTarget}
          onClose={() => {
            setApiKeyTarget(null)
            Promise.all([listRoutes(), listAuthScopes()]).then(([routes, scopes]) => {
              const scopeById = new Map(scopes.filter((s) => s.type === 'env' || s.type === 'client_credentials').map((s) => [s.id, s]))
              const result: Record<string, AuthScope> = {}
              for (const rule of routes) {
                if (rule.auth_scope_id && scopeById.has(rule.auth_scope_id) && rule.downstream_server_id) {
                  result[rule.downstream_server_id] = scopeById.get(rule.auth_scope_id)!
                }
              }
              setServerAuthScopes(result)
            }).catch(() => {})
          }}
          authScopeId={apiKeyTarget.scope.id}
          authScopeName={apiKeyTarget.scope.name}
          serverName={apiKeyTarget.server.name}
          envFields={apiKeyTarget.scope.env_fields}
        />
      )}

    </div>
  )
}

function ModeTabs({
  current,
  installedCount,
  availableCount,
  embedded,
}: {
  current: DownstreamsMode
  installedCount: number
  availableCount: number
  embedded?: boolean
}) {
  const tabs: Array<{ mode: DownstreamsMode; label: string; href: string; count: number; testid: string }> = embedded
    ? [
        { mode: 'installed', label: 'Installed', href: '/workspaces', count: installedCount, testid: 'downstream-tab-installed' },
        { mode: 'available', label: 'Available', href: '/setup', count: availableCount, testid: 'downstream-tab-available' },
      ]
    : [
        { mode: 'installed', label: 'Installed', href: '/workspaces', count: installedCount, testid: 'downstream-tab-installed' },
        { mode: 'available', label: 'Available', href: '/setup', count: availableCount, testid: 'downstream-tab-available' },
      ]
  return (
    <div className="border-b border-border">
      <nav className="-mb-px flex gap-6" aria-label="Servers view">
        {tabs.map((t) => {
          const active = t.mode === current
          return (
            <Link
              key={t.mode}
              to={t.href}
              data-testid={t.testid}
              className={cn(
                'inline-flex items-center gap-2 border-b-2 px-1 pb-2 text-sm font-medium transition-colors',
                active
                  ? 'border-primary text-foreground'
                  : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              {t.label}
              {t.count > 0 && (
                <Badge variant="secondary" className="h-5 min-w-[1.25rem] px-1 text-[10px]">
                  {t.count}
                </Badge>
              )}
            </Link>
          )
        })}
      </nav>
    </div>
  )
}

function EmptyState({ mode }: { mode: DownstreamsMode }) {
  if (mode === 'installed') {
    return (
      <SharedEmptyState
        testid="servers-installed-empty"
        icon={<Server className="h-9 w-9" />}
        title="No servers installed yet"
        description="Browse the catalog to install MCP tool servers with one click, or build a custom one."
        action={
          <Button asChild>
            <Link to="/setup">Browse Available</Link>
          </Button>
        }
      />
    )
  }
  return (
    <SharedEmptyState
      testid="servers-available-empty"
      icon={<Server className="h-9 w-9" />}
      title="Everything in the catalog is installed"
      description={
        <>
          Need something not in the catalog? Use <span className="font-mono text-xs">Add Custom</span> on the Installed view, or ask your agent to help register a custom MCP server.
        </>
      }
    />
  )
}
