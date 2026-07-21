import { useMemo } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { ChevronDown, ChevronRight, Folder, Globe, History, Package, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SkillRegistryEntry, SkillSearchHit } from '@/api/client'
import { groupByCategory, UNCATEGORIZED } from './skill-helpers'

// List — one card with category sections. Section headers are hairline-
// flagged chevrons (no boxy accordion). Sections collapse independently
// and their state is persisted by SkillRegistryPage in localStorage.

interface SkillListProps {
  rows: SkillRegistryEntry[]
  selectedName: string | null
  searchHits: SkillSearchHit[] | null
  collapsed: Set<string>
  groupCategories?: boolean
  onToggleCategory: (category: string) => void
  onSelect: (name: string) => void
  onVersions: (name: string) => void
  onDelete: (entry: SkillRegistryEntry) => void
}

export function SkillList({
  rows,
  selectedName,
  searchHits,
  collapsed,
  groupCategories = true,
  onToggleCategory,
  onSelect,
  onVersions,
  onDelete,
}: SkillListProps) {
  const hitMap = useMemo(() => {
    if (!searchHits) return null
    return new Map(searchHits.map((h) => [h.name, h.score]))
  }, [searchHits])

  const maxScore = useMemo(() => {
    if (!hitMap) return 0
    return Math.max(...Array.from(hitMap.values()), 0.001)
  }, [hitMap])

  if (rows.length === 0 && searchHits) {
    return (
      <Card>
        <CardContent className="px-4 py-10 text-center text-sm text-muted-foreground">
          Nothing matched. Try a different phrasing, or browse the catalog by clearing the search.
        </CardContent>
      </Card>
    )
  }

  // When a search is active we drop the category grouping — the search
  // ranking is the structure that matters and folder headers just add
  // noise to a ranked result list.
  const categoryGroups = !searchHits && groupCategories ? groupByCategory(rows) : []
  const grouped = categoryGroups.length > 1 ? categoryGroups : null

  return (
    <Card className="overflow-hidden">
      <CardContent className="p-0">
        {grouped ? (
          <ul className="divide-y divide-border/40">
            {grouped.map((group) => (
              <CategorySection
                key={group.category}
                category={group.category}
                entries={group.entries}
                collapsed={collapsed.has(group.category)}
                onToggle={() => onToggleCategory(group.category)}
                selectedName={selectedName}
                hitMap={hitMap}
                maxScore={maxScore}
                onSelect={onSelect}
                onVersions={onVersions}
                onDelete={onDelete}
              />
            ))}
          </ul>
        ) : (
          <ul className="divide-y divide-border/40">
            {rows.map((entry) => (
              <li key={entry.id}>
                <SkillRow
                  entry={entry}
                  active={selectedName === entry.name}
                  score={hitMap?.get(entry.name)}
                  scoreMax={maxScore}
                  onSelect={() => onSelect(entry.name)}
                  onVersions={() => onVersions(entry.name)}
                  onDelete={() => onDelete(entry)}
                />
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function CategorySection({
  category,
  entries,
  collapsed,
  onToggle,
  selectedName,
  hitMap,
  maxScore,
  onSelect,
  onVersions,
  onDelete,
}: {
  category: string
  entries: SkillRegistryEntry[]
  collapsed: boolean
  onToggle: () => void
  selectedName: string | null
  hitMap: Map<string, number> | null
  maxScore: number
  onSelect: (name: string) => void
  onVersions: (name: string) => void
  onDelete: (entry: SkillRegistryEntry) => void
}) {
  const label = category === UNCATEGORIZED ? 'uncategorized' : category
  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 px-4 py-2 text-left text-[11px] uppercase tracking-wider text-muted-foreground/80 transition-colors hover:bg-accent/15"
        aria-expanded={!collapsed}
        data-testid={`category-${category}`}
      >
        {collapsed ? (
          <ChevronRight className="h-3 w-3 text-muted-foreground/60" />
        ) : (
          <ChevronDown className="h-3 w-3 text-muted-foreground/60" />
        )}
        <span className="font-medium text-foreground/80">{label}</span>
        <span className="text-muted-foreground/50">{entries.length}</span>
      </button>
      {!collapsed && (
        <ul className="divide-y divide-border/40">
          {entries.map((entry) => (
            <li key={entry.id}>
              <SkillRow
                entry={entry}
                active={selectedName === entry.name}
                score={hitMap?.get(entry.name)}
                scoreMax={maxScore}
                onSelect={() => onSelect(entry.name)}
                onVersions={() => onVersions(entry.name)}
                onDelete={() => onDelete(entry)}
              />
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}

function SkillRow({
  entry,
  active,
  score,
  scoreMax,
  onSelect,
  onVersions,
  onDelete,
}: {
  entry: SkillRegistryEntry
  active: boolean
  score: number | undefined
  scoreMax: number
  onSelect: () => void
  onVersions: () => void
  onDelete: () => void
}) {
  const tags = entry.tags ?? []
  return (
    <div
      className={cn(
        'group flex w-full items-stretch border-l-2 transition-colors',
        active
          ? 'border-l-primary bg-accent/45'
          : 'border-l-transparent hover:bg-accent/20',
      )}
      data-testid={`skill-row-${entry.name}`}
    >
      <button
        type="button"
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-start gap-3 px-3 py-3 text-left"
        aria-current={active ? 'true' : undefined}
      >
        <div className="min-w-0 flex-1 overflow-hidden">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="truncate text-[13px] font-semibold text-foreground">
              {entry.name}
            </span>
            <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
              v{entry.version}
            </span>
            {entry.workspace_id ? (
              <span className="shrink-0 inline-flex items-center gap-1 text-[10px] uppercase tracking-wider text-primary/80">
                <Folder className="h-2.5 w-2.5" />
                ws
              </span>
            ) : (
              <span className="shrink-0 inline-flex items-center gap-1 text-[10px] uppercase tracking-wider text-muted-foreground/70">
                <Globe className="h-2.5 w-2.5" />
                global
              </span>
            )}
            {(entry.source_type === 'path' || entry.source_type === 'bundle' || entry.bundle_sha256) && (
              <span
                className={cn(
                  'shrink-0 inline-flex items-center gap-1 border px-1 py-px text-[9px] uppercase tracking-wider',
                  entry.bundle_sha256
                    ? 'border-emerald-500/30 bg-emerald-500/5 text-emerald-300/90'
                    : 'border-border/60 text-muted-foreground',
                )}
                title={
                  entry.bundle_sha256
                    ? `tar.gz bundle attached — sha256 ${entry.bundle_sha256.slice(0, 12)}…`
                    : 'sidecar files (path source)'
                }
              >
                <Package className="h-2.5 w-2.5" />
                {entry.bundle_sha256 ? 'bundle' : 'path'}
              </span>
            )}
          </div>
          <p className="mt-0.5 line-clamp-2 text-xs leading-relaxed text-muted-foreground">
            {entry.description}
          </p>
          {tags.length > 0 && (
            <div className="mt-1 flex flex-wrap gap-1">
              {tags.slice(0, 6).map((t) => (
                <span
                  key={t}
                  className="text-[9px] text-muted-foreground/70"
                >
                  #{t}
                </span>
              ))}
              {tags.length > 6 && (
                <span className="text-[9px] text-muted-foreground/50">+{tags.length - 6}</span>
              )}
            </div>
          )}
          {entry.author && entry.author !== 'system' && (
            <p className="mt-0.5 truncate text-[10px] text-muted-foreground/60">
              by {entry.author}
            </p>
          )}
        </div>
      </button>
      <div className="flex shrink-0 items-start gap-1 px-2 py-3">
        {score !== undefined && <ScoreMeter score={score} max={scoreMax} />}
        <RowAction label="Versions" onClick={onVersions}>
          <History className="h-3.5 w-3.5" />
        </RowAction>
        <RowAction label="Delete" destructive onClick={onDelete}>
          <Trash2 className="h-3.5 w-3.5" />
        </RowAction>
      </div>
    </div>
  )
}

function RowAction({
  children,
  label,
  destructive,
  onClick,
}: {
  children: React.ReactNode
  label: string
  destructive?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className={cn(
        'grid h-7 w-7 place-items-center text-muted-foreground transition-colors',
        destructive ? 'hover:text-destructive' : 'hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

function ScoreMeter({ score, max }: { score: number; max: number }) {
  // 5-segment dot meter — fills proportionally to score / max.
  const filled = Math.max(1, Math.round((score / max) * 5))
  return (
    <span
      className="mr-1 flex items-center gap-0.5"
      title={`Relevance score ${score.toFixed(3)}`}
      aria-label={`Score ${score.toFixed(2)}`}
    >
      {Array.from({ length: 5 }).map((_, i) => (
        <span
          key={i}
          className={cn(
            'h-1 w-1 transition-colors',
            i < filled ? 'bg-primary' : 'bg-border',
          )}
        />
      ))}
    </span>
  )
}

export function SkillListSkeleton({ count = 8 }: { count?: number }) {
  return (
    <Card className="overflow-hidden py-0">
      <CardContent className="p-0">
        <div className="divide-y divide-border/40">
          {Array.from({ length: count }).map((_, i) => (
            <div key={i} className="animate-pulse px-4 py-3">
              <div className="h-3.5 w-2/5 bg-muted/40" />
              <div className="mt-2 h-3 w-full bg-muted/30" />
              <div className="mt-1 h-3 w-2/3 bg-muted/20" />
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
