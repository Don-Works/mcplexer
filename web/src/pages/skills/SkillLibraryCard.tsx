import { Folder, Globe, History, Package, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import type { SkillRegistryEntry } from '@/api/client'

interface SkillLibraryCardProps {
  entry: SkillRegistryEntry
  active: boolean
  score?: number
  scoreMax: number
  onSelect: () => void
  onVersions: () => void
  onDelete: () => void
}

export function SkillLibraryCard({
  entry,
  active,
  score,
  scoreMax,
  onSelect,
  onVersions,
  onDelete,
}: SkillLibraryCardProps) {
  const tags = entry.tags ?? []
  const hash = entry.content_hash?.slice(0, 8) ?? ''
  const hasBundle = !!entry.bundle_sha256

  return (
    <div
      className={cn(
        'group relative flex w-full flex-col border text-left transition-all',
        active
          ? 'border-primary/60 bg-accent/50 shadow-[0_0_0_1px_rgba(var(--color-primary),0.3)]'
          : 'border-border bg-card hover:border-muted-foreground/40 hover:bg-accent/20',
      )}
      data-testid={`skill-card-${entry.name}`}
    >
      <button type="button" onClick={onSelect} className="flex items-start gap-3 px-4 pt-4 pb-2 text-left">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="truncate text-[13px] font-semibold text-foreground">
              {entry.name}
            </span>
            <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
              v{entry.version}
            </span>
          </div>
          <p className="mt-1 line-clamp-2 text-xs leading-relaxed text-muted-foreground">
            {entry.description}
          </p>
        </div>
        {score !== undefined && scoreMax > 0 && (
          <ScoreBar score={score} max={scoreMax} />
        )}
      </button>

      <div className="flex flex-wrap items-center gap-1.5 px-4 pb-2">
        <ProvenanceBadge sourceType={entry.source_type} hasBundle={hasBundle} />
        {entry.workspace_id ? (
          <Badge variant="outline" tone="muted" className="gap-1 text-[9px]">
            <Folder className="h-2.5 w-2.5" />
            ws
          </Badge>
        ) : (
          <Badge variant="outline" tone="muted" className="gap-1 text-[9px]">
            <Globe className="h-2.5 w-2.5" />
            global
          </Badge>
        )}
        {hash && (
          <Badge variant="outline" tone="mono" className="text-[9px]">
            {hash}
          </Badge>
        )}
        {entry.author && entry.author !== 'system' && (
          <span className="text-[10px] text-muted-foreground/70">
            {entry.author}
          </span>
        )}
      </div>

      {tags.length > 0 && (
        <div className="flex flex-wrap gap-1 px-4 pb-3">
          {tags.slice(0, 5).map((t) => (
            <span
              key={t}
              className="border border-border/60 bg-muted/30 px-1.5 py-0.5 text-[9px] text-muted-foreground"
            >
              #{t}
            </span>
          ))}
          {tags.length > 5 && (
            <span className="text-[9px] text-muted-foreground/50">
              +{tags.length - 5}
            </span>
          )}
        </div>
      )}

      <div className="mt-auto flex items-center justify-between border-t border-border/40 px-4 py-2">
        <span className="text-[10px] text-muted-foreground/60">
          {formatDate(entry.published_at)}
        </span>
        <div
          className={cn(
            'flex items-center gap-0.5 transition-opacity',
            active ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
          )}
        >
          <CardAction label="History" onClick={onVersions}>
            <History className="h-3 w-3" />
          </CardAction>
          <CardAction label="Delete" destructive onClick={onDelete}>
            <Trash2 className="h-3 w-3" />
          </CardAction>
        </div>
      </div>
    </div>
  )
}

function ProvenanceBadge({
  sourceType,
  hasBundle,
}: {
  sourceType?: string
  hasBundle: boolean
}) {
  if (hasBundle) {
    return (
      <Badge
        variant="outline"
        className="gap-1 border-emerald-500/30 bg-emerald-500/5 text-[9px] text-emerald-300/90"
      >
        <Package className="h-2.5 w-2.5" />
        bundle
      </Badge>
    )
  }
  if (sourceType === 'path') {
    return (
      <Badge variant="outline" tone="muted" className="gap-1 text-[9px]">
        <Package className="h-2.5 w-2.5" />
        path
      </Badge>
    )
  }
  if (sourceType === 'inline') {
    return (
      <Badge variant="outline" tone="muted" className="text-[9px]">
        inline
      </Badge>
    )
  }
  if (sourceType === 'git') {
    return (
      <Badge variant="outline" tone="muted" className="text-[9px]">
        git
      </Badge>
    )
  }
  return null
}

function ScoreBar({ score, max }: { score: number; max: number }) {
  const ratio = max > 0 ? score / max : 0
  const pct = Math.round(ratio * 100)
  return (
    <div
      className="shrink-0 self-start"
      title={`Relevance: ${pct}%`}
      aria-label={`Score ${pct}%`}
    >
      <div className="h-1.5 w-12 bg-border">
        <div
          className="h-full bg-primary transition-all"
          style={{ width: `${Math.max(4, pct)}%` }}
        />
      </div>
    </div>
  )
}

function CardAction({
  children,
  label,
  destructive,
  onClick,
}: {
  children: React.ReactNode
  label: string
  destructive?: boolean
  onClick: (e: React.MouseEvent) => void
}) {
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation()
        onClick(e)
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onClick(e as unknown as React.MouseEvent)
        }
      }}
      aria-label={label}
      className={cn(
        'grid h-6 w-6 cursor-pointer place-items-center text-muted-foreground transition-colors',
        destructive ? 'hover:text-destructive' : 'hover:text-foreground',
      )}
    >
      {children}
    </button>
  )
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}
