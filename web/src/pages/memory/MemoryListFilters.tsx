// MemoryListFilters — scope + kind filter chips for /memory/all.
// Multi-select tag chips live in the parent because they're driven by
// the actual list response.

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Check, Filter, X } from 'lucide-react'
import { cn } from '@/lib/utils'

export type ScopeFilter = 'all' | 'workspace' | 'global' | 'peer'
export type KindFilter = 'all' | 'fact' | 'note'

interface Props {
  scope: ScopeFilter
  onScope: (v: ScopeFilter) => void
  kind: KindFilter
  onKind: (v: KindFilter) => void
  selectedTags: string[]
  availableTags: string[]
  onToggleTag: (t: string) => void
  onClearTags: () => void
  includeInvalid: boolean
  onToggleInvalid: () => void
  staleOnly: boolean
  onToggleStale: () => void
}

export function MemoryListFilters(props: Props) {
  const {
    scope,
    onScope,
    kind,
    onKind,
    selectedTags,
    availableTags,
    onToggleTag,
    onClearTags,
    includeInvalid,
    onToggleInvalid,
    staleOnly,
    onToggleStale,
  } = props
  const anyActive =
    scope !== 'all' ||
    kind !== 'all' ||
    selectedTags.length > 0 ||
    includeInvalid ||
    staleOnly
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="mr-1 inline-flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
        <Filter className="h-3 w-3" />
        Filters
      </span>

      <ScopeChip value={scope} onSet={onScope} />
      <KindChip value={kind} onSet={onKind} />
      <TagChip
        selected={selectedTags}
        available={availableTags}
        onToggle={onToggleTag}
        onClear={onClearTags}
      />
      <InvalidChip on={includeInvalid} onToggle={onToggleInvalid} />
      <StaleChip on={staleOnly} onToggle={onToggleStale} />

      {anyActive && (
        <button
          type="button"
          onClick={() => {
            onScope('all')
            onKind('all')
            onClearTags()
            if (includeInvalid) onToggleInvalid()
            if (staleOnly) onToggleStale()
          }}
          data-testid="memory-filters-clear"
          className="ml-1 text-[11px] text-muted-foreground hover:text-foreground"
        >
          Clear filters
        </button>
      )}
    </div>
  )
}

function Chip({
  active,
  children,
  testid,
  onClear,
}: {
  active: boolean
  children: React.ReactNode
  testid?: string
  onClear?: () => void
}) {
  return (
    <span
      data-testid={testid}
      className={cn(
        'inline-flex items-center gap-1 border px-2 py-1 font-mono text-[12px] transition-colors',
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

function ScopeChip({
  value,
  onSet,
}: {
  value: ScopeFilter
  onSet: (v: ScopeFilter) => void
}) {
  const active = value !== 'all'
  const opts: ScopeFilter[] = ['all', 'workspace', 'global', 'peer']
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" data-testid="memory-filter-scope">
          <Chip active={active} onClear={() => onSet('all')}>
            {active ? (
              <>
                scope = <span className="text-primary">{value}</span>
              </>
            ) : (
              '+ Scope'
            )}
          </Chip>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {opts.map((o) => (
          <DropdownMenuItem key={o} onClick={() => onSet(o)}>
            <Check className={cn('mr-2 h-3 w-3', value === o ? 'opacity-100' : 'opacity-0')} />
            <span className="capitalize">{o}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function KindChip({
  value,
  onSet,
}: {
  value: KindFilter
  onSet: (v: KindFilter) => void
}) {
  const active = value !== 'all'
  const opts: KindFilter[] = ['all', 'fact', 'note']
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" data-testid="memory-filter-kind">
          <Chip active={active} onClear={() => onSet('all')}>
            {active ? (
              <>
                kind = <span className="text-primary">{value}</span>
              </>
            ) : (
              '+ Kind'
            )}
          </Chip>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {opts.map((o) => (
          <DropdownMenuItem key={o} onClick={() => onSet(o)}>
            <Check className={cn('mr-2 h-3 w-3', value === o ? 'opacity-100' : 'opacity-0')} />
            <span className="capitalize">{o}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function TagChip({
  selected,
  available,
  onToggle,
  onClear,
}: {
  selected: string[]
  available: string[]
  onToggle: (t: string) => void
  onClear: () => void
}) {
  const active = selected.length > 0
  const label = active
    ? selected.length === 1
      ? `tag = ${selected[0]}`
      : `tags = ${selected.length}`
    : '+ Tag'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" data-testid="memory-filter-tag">
          <Chip active={active} onClear={onClear}>
            {active ? <span className="text-primary">{label}</span> : label}
          </Chip>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-[260px] min-w-[12rem] overflow-y-auto">
        {available.length === 0 && (
          <DropdownMenuItem disabled>
            <span className="text-muted-foreground">No tags in current view</span>
          </DropdownMenuItem>
        )}
        {available.map((t) => {
          const on = selected.includes(t)
          return (
            <DropdownMenuItem
              key={t}
              onSelect={(e) => {
                e.preventDefault()
                onToggle(t)
              }}
            >
              <Check className={cn('mr-2 h-3 w-3', on ? 'opacity-100' : 'opacity-0')} />
              <span className="font-mono text-[12px]">{t}</span>
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function InvalidChip({ on, onToggle }: { on: boolean; onToggle: () => void }) {
  return (
    <button type="button" onClick={onToggle} data-testid="memory-filter-invalid">
      <Chip active={on} onClear={on ? onToggle : undefined}>
        {on ? (
          <span className="text-primary">+ invalidated rows</span>
        ) : (
          'Hide invalidated'
        )}
      </Chip>
    </button>
  )
}

function StaleChip({ on, onToggle }: { on: boolean; onToggle: () => void }) {
  return (
    <button type="button" onClick={onToggle} data-testid="memory-filter-stale">
      <Chip active={on} onClear={on ? onToggle : undefined}>
        {on ? (
          <span className="text-primary">stale · needs review</span>
        ) : (
          '+ Stale'
        )}
      </Chip>
    </button>
  )
}
