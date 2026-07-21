import { useMemo, useState } from 'react'
import { ChevronDown, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { SkillRegistryEntry } from '@/api/client'
import { tagFrequency } from './skill-helpers'

interface Props {
  entries: SkillRegistryEntry[]
  selected: Set<string>
  onToggle: (tag: string) => void
  onClear: () => void
}

// Visible-chip cap. Above this we collapse the tail into a "more" popover
// so the bar never wraps onto a second row and crowds the scope filter.
const VISIBLE_TAGS = 7

// TagBar surfaces the tag space across the currently-visible scope as a
// row of clickable chips. Multi-select with AND semantics — every active
// tag narrows the list further. Empty state stays out of the way: if no
// entry advertises a tag at all, the bar just doesn't render.
export function TagBar({ entries, selected, onToggle, onClear }: Props) {
  const freq = useMemo(() => tagFrequency(entries), [entries])
  if (freq.length === 0) return null

  const visible = freq.slice(0, VISIBLE_TAGS)
  const overflow = freq.slice(VISIBLE_TAGS)

  return (
    <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
      <span className="mr-1 text-muted-foreground/60 uppercase tracking-wider">Tags</span>
      {visible.map(({ tag, count }) => (
        <TagChip
          key={tag}
          tag={tag}
          count={count}
          active={selected.has(tag)}
          onToggle={() => onToggle(tag)}
        />
      ))}
      {overflow.length > 0 && (
        <OverflowMenu items={overflow} selected={selected} onToggle={onToggle} />
      )}
      {selected.size > 0 && (
        <button
          type="button"
          onClick={onClear}
          className="ml-1 text-[10px] text-muted-foreground/70 underline-offset-2 hover:text-foreground hover:underline"
          data-testid="tag-clear"
        >
          clear ({selected.size})
        </button>
      )}
    </div>
  )
}

function TagChip({
  tag,
  count,
  active,
  onToggle,
}: {
  tag: string
  count: number
  active: boolean
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      data-testid={`tag-${tag}`}
      className={cn(
        'inline-flex items-center gap-1 border px-2 py-0.5 transition-colors',
        active
          ? 'border-primary/60 bg-accent/40 text-foreground'
          : 'border-border text-muted-foreground hover:border-muted-foreground hover:text-foreground',
      )}
    >
      <span>#{tag}</span>
      <span className="text-[9px] text-muted-foreground/60">{count}</span>
      {active && <X className="h-2.5 w-2.5 text-foreground/70" />}
    </button>
  )
}

function OverflowMenu({
  items,
  selected,
  onToggle,
}: {
  items: { tag: string; count: number }[]
  selected: Set<string>
  onToggle: (tag: string) => void
}) {
  const [open, setOpen] = useState(false)
  // How many of the hidden tags are currently active — surfaces in the
  // trigger so a user can tell their filter has tags they can't see.
  const hiddenActive = items.filter((t) => selected.has(t.tag)).length
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger
        className={cn(
          'inline-flex items-center gap-1 border border-dashed border-border px-2 py-0.5 text-[11px] text-muted-foreground transition-colors',
          'hover:border-muted-foreground hover:text-foreground',
          hiddenActive > 0 && 'border-primary/40 text-foreground',
        )}
        data-testid="tag-overflow"
      >
        <span>··more</span>
        {hiddenActive > 0 && (
          <span className="text-[9px] text-primary/80">+{hiddenActive}</span>
        )}
        <ChevronDown className="h-2.5 w-2.5" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-64 min-w-[12rem] overflow-y-auto">
        {items.map(({ tag, count }) => (
          <DropdownMenuItem
            key={tag}
            onSelect={(e) => {
              // Keep the menu open so users can stack a few clicks in one
              // gesture — Radix closes on select by default.
              e.preventDefault()
              onToggle(tag)
            }}
            className={cn(
              'flex items-center gap-2 text-[11px]',
              selected.has(tag) && 'bg-accent/40 text-foreground',
            )}
          >
            <span className="flex-1">#{tag}</span>
            <span className="text-[10px] text-muted-foreground/60">{count}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
