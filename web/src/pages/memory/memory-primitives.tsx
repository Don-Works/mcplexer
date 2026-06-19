// memory-primitives — presentational components shared across memory pages.
// Non-component helpers live alongside in memory-utils.ts. Keep this file
// component-only to satisfy the react-refresh/only-export-components rule
// (Vite HMR needs every export here to be a React component).

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { MemoryKind, MemorySourceKind } from '@/api/memory'
import type { MemoryScope } from './memory-utils'

export function ScopeBadge({ scope }: { scope: MemoryScope }) {
  const tone =
    scope === 'workspace' ? 'info' : scope === 'peer' ? 'success' : 'muted'
  return (
    <Badge variant="outline" tone={tone} className="text-[10px] uppercase tracking-wider">
      {scope}
    </Badge>
  )
}

export function KindBadge({ kind }: { kind: MemoryKind }) {
  const tone = kind === 'fact' ? 'info' : 'muted'
  return (
    <Badge variant="outline" tone={tone} className="text-[10px] uppercase tracking-wider">
      {kind}
    </Badge>
  )
}

export function SourceChip({ source }: { source: MemorySourceKind }) {
  const tone =
    source === 'human'
      ? 'info'
      : source === 'worker'
        ? 'warn'
        : source === 'peer'
          ? 'success'
          : 'muted'
  return (
    <Badge variant="outline" tone={tone} className="text-[10px] font-mono">
      {source}
    </Badge>
  )
}

export function TagChips({
  tags,
  max,
}: {
  tags?: string[] | null
  max?: number
}) {
  if (!tags || tags.length === 0) {
    return <span className="text-[11px] text-muted-foreground/40">—</span>
  }
  const limit = max ?? 4
  const visible = tags.slice(0, limit)
  const overflow = tags.length - visible.length
  return (
    <div className="flex flex-wrap items-center gap-1">
      {visible.map((t) => (
        <span
          key={t}
          className="inline-flex items-center border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
        >
          {t}
        </span>
      ))}
      {overflow > 0 && (
        <span className="font-mono text-[10px] text-muted-foreground/60">
          +{overflow}
        </span>
      )}
    </div>
  )
}

// PreviewSnippet renders the truncated content with monospace baseline.
// Inlines the trim logic to keep this file component-only.
export function PreviewSnippet({
  content,
  className,
}: {
  content: string
  className?: string
}) {
  const stripped = content.replace(/^---\n[\s\S]*?\n---\n*/, '')
  const flat = stripped.replace(/\s+/g, ' ').trim()
  const text = flat.length <= 120 ? flat : flat.slice(0, 119).trimEnd() + '…'
  return (
    <span
      className={cn(
        'block truncate text-[12px] text-muted-foreground/90',
        className,
      )}
      title={content.length > 200 ? content.slice(0, 200) + '…' : content}
    >
      {text}
    </span>
  )
}
