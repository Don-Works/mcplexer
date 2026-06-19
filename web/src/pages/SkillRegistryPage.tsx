import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { useApi } from '@/hooks/use-api'
import {
  deleteSkillRegistry,
  getHealth,
  listSkillRegistry,
  listWorkspaces,
  searchSkillRegistry,
  type SkillRegistryEntry,
  type SkillScopeFilter,
  type SkillSearchHit,
} from '@/api/client'
import {
  Loader2,
  Network,
  Package,
  Plus,
  SearchX,
  Server,
  Sparkles,
  WifiOff,
  X,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { toast } from 'sonner'
import { EmptyState as SharedEmptyState } from '@/components/ui/empty-state'

import { SearchBlock } from './skills/SearchBlock'
import { ScopeFilter } from './skills/ScopeFilter'
import { SkillList, SkillListSkeleton } from './skills/SkillList'
import { SkillDetailPane } from './skills/SkillDetailPane'
import { PublishDialog } from './skills/PublishDialog'
import { VersionsDialog } from './skills/VersionsDialog'
import { TagBar } from './skills/TagBar'
import { LocalSkillsMigrationTile } from './skills/LocalSkillsMigrationTile'
import { matchesTagFilter } from './skills/skill-helpers'
import { isServerProfile } from '@/lib/server-profile'

const COLLAPSED_STORAGE_KEY = 'mcplexer:skills:collapsedCategories'

export function SkillRegistryPage() {
  const [scopeFilter, setScopeFilter] = useState<SkillScopeFilter>({ mode: 'all' })
  const fetcher = useCallback(() => listSkillRegistry(scopeFilter), [scopeFilter])
  const { data: heads, loading, error, refetch } = useApi(fetcher)
  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)
  const healthFetcher = useCallback(() => getHealth().catch(() => null), [])
  const { data: health } = useApi(healthFetcher)
  const serverMode = isServerProfile(health?.system)

  const [query, setQuery] = useState('')
  const [searchHits, setSearchHits] = useState<SkillSearchHit[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState<string | null>(null)

  const [selectedName, setSelectedName] = useState<string | null>(null)
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false)
  const [publishOpen, setPublishOpen] = useState(false)
  const [versionsTarget, setVersionsTarget] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<SkillRegistryEntry | null>(null)

  const [selectedTags, setSelectedTags] = useState<Set<string>>(() => new Set())

  const [collapsedCategories, setCollapsedCategories] = useState<Set<string>>(() => {
    if (typeof window === 'undefined') return new Set()
    try {
      const raw = window.localStorage.getItem(COLLAPSED_STORAGE_KEY)
      if (!raw) return new Set()
      const arr = JSON.parse(raw)
      return Array.isArray(arr) ? new Set(arr.filter((s) => typeof s === 'string')) : new Set()
    } catch {
      return new Set()
    }
  })
  useEffect(() => {
    if (typeof window === 'undefined') return
    try {
      window.localStorage.setItem(
        COLLAPSED_STORAGE_KEY,
        JSON.stringify(Array.from(collapsedCategories)),
      )
    } catch {
      // localStorage persistence is best-effort; ignore quota/private-mode failures.
    }
  }, [collapsedCategories])

  useEffect(() => {
    if (!selectedName && heads && heads.length > 0) {
      setSelectedName(heads[0].name)
    }
  }, [heads, selectedName])

  const visibleRows = useMemo(() => {
    if (!heads) return []
    const tagFiltered = heads.filter((e) => matchesTagFilter(e, selectedTags))
    if (!searchHits) return tagFiltered
    const order = new Map(searchHits.map((h, i) => [h.name, i]))
    return tagFiltered
      .filter((e) => order.has(e.name))
      .sort((a, b) => (order.get(a.name) ?? 0) - (order.get(b.name) ?? 0))
  }, [heads, searchHits, selectedTags])

  async function runSearch(q: string) {
    if (!q.trim()) {
      setSearchHits(null)
      setSearchError(null)
      return
    }
    setSearching(true)
    setSearchError(null)
    try {
      const hits = await searchSkillRegistry(q, 20)
      setSearchHits(hits)
      if (hits.length > 0) setSelectedName(hits[0].name)
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Search failed'
      setSearchError(msg)
      toast.error(msg)
    } finally {
      setSearching(false)
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return
    try {
      await deleteSkillRegistry(deleteTarget.name, deleteTarget.version)
      toast.success(`Deleted ${deleteTarget.name}@${deleteTarget.version}`)
      if (selectedName === deleteTarget.name) setSelectedName(null)
      setDeleteTarget(null)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    }
  }

  function toggleTag(tag: string) {
    setSelectedTags((prev) => {
      const next = new Set(prev)
      if (next.has(tag)) next.delete(tag)
      else next.add(tag)
      return next
    })
  }

  function toggleCategory(category: string) {
    setCollapsedCategories((prev) => {
      const next = new Set(prev)
      if (next.has(category)) next.delete(category)
      else next.add(category)
      return next
    })
  }

  function handleSelectSkill(name: string) {
    setSelectedName(name)
    setMobileDetailOpen(true)
  }

  const bundleCount = heads?.filter((h) => !!h.bundle_sha256).length ?? 0
  const totalCount = heads?.length ?? 0

  const isSearchEmpty = searchHits && searchHits.length === 0
  const isApiError = !!error
  const isEmbedderDown = searchError && searchError.includes('embedder')
  const isOffline = error && (error.includes('fetch') || error.includes('network'))

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">
            {serverMode ? 'Skills Repository' : 'Skills Library'}
          </h1>
          {totalCount > 0 && (
            <div
              data-testid="skills-header-stats"
              className="mt-2 flex flex-wrap items-center gap-2 text-[11px] uppercase tracking-wider text-muted-foreground"
            >
              <span className="font-mono tabular-nums">{totalCount} skills</span>
              <span className="text-muted-foreground/40">/</span>
              <span
                title="Skills with tar.gz sidecars attached"
                className={cn(
                  'inline-flex items-center gap-1 font-mono tabular-nums',
                  bundleCount > 0 ? 'text-foreground/70' : 'text-muted-foreground/50',
                )}
              >
                <Package className="h-3 w-3" />
                {bundleCount} with files
              </span>
            </div>
          )}
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            {serverMode ? (
              <>
                Global, workspace-agnostic skills for local MCPlexer instances to search and pull.
                Publishing here is an explicit repository action.
              </>
            ) : (
              <>
                A library of <span className="text-foreground">how to do X</span> recipes that any agent
                can ask for, read, and add to. Same surface as{' '}
                <code className="text-[11px] text-foreground/80">mcpx__skill_search</code>.
              </>
            )}
          </p>
        </div>
        <div className="flex shrink-0 items-end gap-2">
          <Link to="/skills/graph">
            <Button variant="ghost" size="sm">
              <Network className="mr-1.5 h-3 w-3" />
              Graph
            </Button>
          </Link>
          <Button onClick={() => setPublishOpen(true)} data-testid="skill-publish">
            <Plus className="mr-2 h-4 w-4" />
            {serverMode ? 'New global skill' : 'New skill'}
          </Button>
        </div>
      </header>

      {!serverMode && <LocalSkillsMigrationTile onImported={refetch} />}

      <SearchBlock
        value={query}
        onChange={setQuery}
        onSubmit={runSearch}
        searching={searching}
        hits={searchHits}
        onClear={() => {
          setQuery('')
          setSearchHits(null)
          setSearchError(null)
        }}
      />

      {isEmbedderDown && (
        <FallbackBanner
          icon={<SearchX className="h-4 w-4" />}
          title="Semantic search unavailable"
          description="The embedding model is not configured or unreachable. Falling back to lexical search. Try simpler keyword queries."
        />
      )}

      {isOffline && (
        <FallbackBanner
          icon={<WifiOff className="h-4 w-4" />}
          title="Connection lost"
          description="Cannot reach the gateway. Showing cached data if available."
        />
      )}

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        {!serverMode && (
          <ScopeFilter
            value={scopeFilter}
            onChange={setScopeFilter}
            workspaces={workspaces ?? []}
          />
        )}
        <TagBar
          entries={heads ?? []}
          selected={selectedTags}
          onToggle={toggleTag}
          onClear={() => setSelectedTags(new Set())}
        />
      </div>

      {isApiError && !isOffline && (
        <div className="border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive">
          <div className="flex items-center gap-2">
            <Server className="h-4 w-4 shrink-0" />
            <span>API error: {error}</span>
          </div>
        </div>
      )}

      {loading && !heads && (
        <div className="space-y-4">
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            Reading the registry…
          </div>
          <SkillListSkeleton count={8} />
        </div>
      )}

      {heads && heads.length === 0 && !isSearchEmpty && (
        <EmptyState serverMode={serverMode} onPublish={() => setPublishOpen(true)} />
      )}

      {heads && heads.length > 0 && (
        <div className="grid gap-5 lg:grid-cols-[minmax(0,400px)_minmax(0,1fr)]">
          <section className="min-w-0 space-y-3" aria-label="Skill catalog">
            <CatalogSummary
              visibleCount={visibleRows.length}
              totalCount={totalCount}
              searchActive={!!searchHits}
              selectedTags={selectedTags.size}
              serverMode={serverMode}
            />
            <SkillList
              rows={visibleRows}
              selectedName={selectedName}
              searchHits={searchHits}
              collapsed={collapsedCategories}
              groupCategories={!serverMode}
              onToggleCategory={toggleCategory}
              onSelect={handleSelectSkill}
              onVersions={setVersionsTarget}
              onDelete={setDeleteTarget}
            />
          </section>
          <aside className="hidden min-w-0 lg:block" aria-label="Skill viewer">
            <div className="sticky top-16 max-h-[calc(100vh-5rem)] overflow-y-auto">
              <SkillDetailPane
                name={selectedName}
                onTagSet={refetch}
                onVersions={setVersionsTarget}
                onDelete={setDeleteTarget}
              />
            </div>
          </aside>
        </div>
      )}

      {mobileDetailOpen && selectedName && heads && heads.length > 0 && (
        <div className="lg:hidden">
          <MobileDetailSheet
            name={selectedName}
            onTagSet={refetch}
            onVersions={setVersionsTarget}
            onDelete={setDeleteTarget}
            onClose={() => setMobileDetailOpen(false)}
          />
        </div>
      )}

      <PublishDialog
        open={publishOpen}
        onOpenChange={setPublishOpen}
        serverMode={serverMode}
        onPublished={() => {
          setPublishOpen(false)
          refetch()
        }}
      />

      <VersionsDialog
        name={versionsTarget}
        onOpenChange={(open) => !open && setVersionsTarget(null)}
        onTagSet={refetch}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`Delete ${deleteTarget?.name}@${deleteTarget?.version}?`}
        description="Soft-delete this version. Other versions are kept; the next-highest active version becomes the head."
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={handleDelete}
      />
    </div>
  )
}

function CatalogSummary({
  visibleCount,
  totalCount,
  searchActive,
  selectedTags,
  serverMode,
}: {
  visibleCount: number
  totalCount: number
  searchActive: boolean
  selectedTags: number
  serverMode: boolean
}) {
  const narrowed = searchActive || selectedTags > 0
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border border-border/70 bg-card/40 px-3 py-2">
      <div className="min-w-0">
        <p className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
          {serverMode ? 'Repository catalog' : 'Catalog'}
        </p>
        <p className="mt-0.5 text-sm text-foreground">
          {narrowed ? `${visibleCount} of ${totalCount} skills` : `${totalCount} skills`}
        </p>
      </div>
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
        {searchActive && <span>search ranked</span>}
        {selectedTags > 0 && <span>{selectedTags} tag filter{selectedTags === 1 ? '' : 's'}</span>}
      </div>
    </div>
  )
}

function EmptyState({ serverMode, onPublish }: { serverMode: boolean; onPublish: () => void }) {
  return (
    <SharedEmptyState
      testid="skills-empty"
      icon={<Sparkles className="h-6 w-6" />}
      title={serverMode ? 'No global skills yet' : 'No skills yet'}
      description={
        serverMode ? (
          <>Publish a global skill here when you want the central repository to own it.</>
        ) : (
          <>
            Skills are <code className="text-[11px] text-foreground/80">SKILL.md</code> recipes that
            teach an agent how to do one specific task. Publish your first one, or have an agent call{' '}
            <code className="text-[11px] text-foreground/80">mcpx__skill_publish</code>.
          </>
        )
      }
      action={
        <Button onClick={onPublish} data-testid="skill-publish-empty">
          <Plus className="mr-2 h-4 w-4" />
          {serverMode ? 'Publish global skill' : 'Publish a skill'}
        </Button>
      }
    />
  )
}

function FallbackBanner({
  icon,
  title,
  description,
}: {
  icon: React.ReactNode
  title: string
  description: string
}) {
  return (
    <div className="flex items-start gap-3 border border-amber-500/30 bg-amber-500/5 px-4 py-3">
      <span className="mt-0.5 shrink-0 text-amber-500">{icon}</span>
      <div>
        <p className="text-sm font-medium text-foreground">{title}</p>
        <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
      </div>
    </div>
  )
}

function MobileDetailSheet({
  name,
  onTagSet,
  onVersions,
  onDelete,
  onClose,
}: {
  name: string
  onTagSet: () => void
  onVersions: (name: string) => void
  onDelete: (entry: SkillRegistryEntry) => void
  onClose: () => void
}) {
  return (
    <div className="fixed inset-x-0 bottom-0 top-[calc(3rem+env(safe-area-inset-top))] z-40 flex flex-col overflow-hidden border-t border-border bg-background lg:hidden">
      <div className="sticky top-0 z-10 flex shrink-0 items-center gap-3 border-b border-border bg-background/95 px-4 py-2 backdrop-blur-sm">
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground">{name}</span>
        <button
          type="button"
          onClick={onClose}
          className="grid h-8 w-8 shrink-0 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          aria-label="Close detail"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto p-4 pb-[calc(1rem+env(safe-area-inset-bottom))]">
        <SkillDetailPane
          name={name}
          onTagSet={onTagSet}
          onVersions={onVersions}
          onDelete={onDelete}
        />
      </div>
    </div>
  )
}
