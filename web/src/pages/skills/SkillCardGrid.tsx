import { useMemo } from 'react'
import { cn } from '@/lib/utils'
import type { SkillRegistryEntry, SkillSearchHit } from '@/api/client'
import { SkillLibraryCard } from './SkillLibraryCard'
import { groupByCategory, UNCATEGORIZED } from './skill-helpers'
import { ChevronDown, ChevronRight } from 'lucide-react'

interface SkillCardGridProps {
  rows: SkillRegistryEntry[]
  selectedName: string | null
  searchHits: SkillSearchHit[] | null
  collapsed: Set<string>
  onToggleCategory: (category: string) => void
  onSelect: (name: string) => void
  onVersions: (name: string) => void
  onDelete: (entry: SkillRegistryEntry) => void
}

export function SkillCardGrid({
  rows,
  selectedName,
  searchHits,
  collapsed,
  onToggleCategory,
  onSelect,
  onVersions,
  onDelete,
}: SkillCardGridProps) {
  const hitMap = useMemo(() => {
    if (!searchHits) return null
    return new Map(searchHits.map((h) => [h.name, h.score]))
  }, [searchHits])

  const maxScore = useMemo(() => {
    if (!hitMap) return 0
    return Math.max(...Array.from(hitMap.values()), 0.001)
  }, [hitMap])

  if (rows.length === 0 && searchHits) {
    return <NoResults />
  }

  const grouped = searchHits ? null : groupByCategory(rows)

  return (
    <div className="space-y-4">
      {grouped ? (
        grouped.map((group) => (
          <CategoryGroup
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
        ))
      ) : (
        <CardGrid
          entries={rows}
          selectedName={selectedName}
          hitMap={hitMap}
          maxScore={maxScore}
          onSelect={onSelect}
          onVersions={onVersions}
          onDelete={onDelete}
        />
      )}
    </div>
  )
}

function CategoryGroup({
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
    <div>
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 py-2 text-left text-[11px] uppercase tracking-wider text-muted-foreground/80 transition-colors hover:text-foreground"
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
        <CardGrid
          entries={entries}
          selectedName={selectedName}
          hitMap={hitMap}
          maxScore={maxScore}
          onSelect={onSelect}
          onVersions={onVersions}
          onDelete={onDelete}
        />
      )}
    </div>
  )
}

function CardGrid({
  entries,
  selectedName,
  hitMap,
  maxScore,
  onSelect,
  onVersions,
  onDelete,
}: {
  entries: SkillRegistryEntry[]
  selectedName: string | null
  hitMap: Map<string, number> | null
  maxScore: number
  onSelect: (name: string) => void
  onVersions: (name: string) => void
  onDelete: (entry: SkillRegistryEntry) => void
}) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {entries.map((entry) => (
        <SkillLibraryCard
          key={entry.id}
          entry={entry}
          active={selectedName === entry.name}
          score={hitMap?.get(entry.name)}
          scoreMax={maxScore}
          onSelect={() => onSelect(entry.name)}
          onVersions={() => onVersions(entry.name)}
          onDelete={() => onDelete(entry)}
        />
      ))}
    </div>
  )
}

function NoResults() {
  return (
    <div className="flex flex-col items-center justify-center border border-dashed border-border bg-card/30 px-6 py-16 text-center">
      <p className="text-sm text-muted-foreground">
        Nothing matched. Try a different phrasing, or browse the catalog by clearing the search.
      </p>
    </div>
  )
}

export function SkillCardSkeleton() {
  return (
    <div className="flex animate-pulse flex-col border border-border bg-card">
      <div className="px-4 pt-4 pb-2">
        <div className="h-4 w-2/3 bg-muted/40" />
        <div className="mt-2 h-3 w-full bg-muted/30" />
        <div className="mt-1 h-3 w-4/5 bg-muted/30" />
      </div>
      <div className="flex gap-1.5 px-4 pb-2">
        <div className="h-4 w-12 bg-muted/30" />
        <div className="h-4 w-10 bg-muted/30" />
      </div>
      <div className="flex gap-1 px-4 pb-3">
        <div className="h-4 w-10 bg-muted/20" />
        <div className="h-4 w-14 bg-muted/20" />
      </div>
      <div className="mt-auto border-t border-border/40 px-4 py-2">
        <div className="h-3 w-20 bg-muted/20" />
      </div>
    </div>
  )
}

export function SkillCardGridSkeleton({ count = 6 }: { count?: number }) {
  return (
    <div className={cn('grid gap-3', count <= 2 ? 'sm:grid-cols-2' : 'sm:grid-cols-2 lg:grid-cols-3')}>
      {Array.from({ length: count }).map((_, i) => (
        <SkillCardSkeleton key={i} />
      ))}
    </div>
  )
}
