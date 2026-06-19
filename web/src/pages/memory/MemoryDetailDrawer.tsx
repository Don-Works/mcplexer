// MemoryDetailDrawer — side-sheet that mirrors AuditDetailDialog's
// information-architecture and j/k keyboard nav. Renders one memory:
// metadata strip → markdown content → actions → provenance expandable.
//
// Sub-components (Section/KV/MemoryActions/DrawerFooterNav/ProvenanceSection)
// live in MemoryDetailSections.tsx to keep this file under 300 lines.

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { CopyButton } from '@/components/ui/copy-button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Markdown, stripFrontmatter } from '@/lib/markdown'
import { Pin, X } from 'lucide-react'
import { toast } from 'sonner'
import type {
  MemoryEntry,
  MemoryEntityRow,
  MemorySuggestion,
} from '@/api/memory'
import {
  listMemoryEntities,
  unlinkMemoryEntity,
  memorySuggestions,
} from '@/api/memory'
import { KindBadge, ScopeBadge, SourceChip } from './memory-primitives'
import { parseTags, relativeTime, scopeOf } from './memory-utils'
import {
  DrawerFooterNav,
  KV,
  MemoryActions,
  ProvenanceSection,
  Section,
} from './MemoryDetailSections'

interface Props {
  entry: MemoryEntry | null
  onClose: () => void
  onPrev?: () => void
  onNext?: () => void
  hasPrev?: boolean
  hasNext?: boolean
  onInvalidate?: (id: string) => Promise<void>
  onDelete?: (id: string) => Promise<void>
  // Pin is optional — backend does not yet expose a toggle endpoint, so
  // when no handler is supplied the button is rendered disabled with a
  // "coming soon" tooltip rather than removed.
  onTogglePin?: (id: string, next: boolean) => Promise<void>
}

export function MemoryDetailDrawer({
  entry,
  onClose,
  onPrev,
  onNext,
  hasPrev,
  hasNext,
  onInvalidate,
  onDelete,
  onTogglePin,
}: Props) {
  const [busy, setBusy] = useState<'invalidate' | 'delete' | 'pin' | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmInvalidate, setConfirmInvalidate] = useState(false)
  const [showProvenance, setShowProvenance] = useState(false)
  // Entity links (migration 076) — what this memory is ABOUT. Fetched
  // on open so we don't pay the round-trip for memories the user never
  // expands. Click-through goes to /memory/about/:kind/:id.
  const [entities, setEntities] = useState<MemoryEntityRow[] | null>(null)
  const [unlinkingKey, setUnlinkingKey] = useState<string | null>(null)
  // AR5 — "you might also remember" bundle. Fetched on open; empty when
  // none of the three signal axes (co-recall, related-entity, semantic)
  // have anything for this memory.
  const [suggestions, setSuggestions] = useState<MemorySuggestion[] | null>(null)
  useEffect(() => {
    if (!entry) {
      setSuggestions(null)
      return
    }
    let cancelled = false
    memorySuggestions(entry.id, 8)
      .then((rows) => {
        if (!cancelled) setSuggestions(rows)
      })
      .catch(() => {
        if (!cancelled) setSuggestions([])
      })
    return () => {
      cancelled = true
    }
  }, [entry?.id])
  useEffect(() => {
    if (!entry) {
      setEntities(null)
      return
    }
    let cancelled = false
    listMemoryEntities(entry.id)
      .then((rows) => {
        if (!cancelled) setEntities(rows)
      })
      .catch(() => {
        if (!cancelled) setEntities([])
      })
    return () => {
      cancelled = true
    }
  }, [entry?.id])

  // Keyboard nav while the drawer is open. Mirrors AuditDetailDialog.
  useEffect(() => {
    if (!entry) return
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      if (
        target &&
        (target.tagName === 'INPUT' ||
          target.tagName === 'TEXTAREA' ||
          target.isContentEditable)
      ) {
        return
      }
      if ((e.key === 'j' || e.key === 'ArrowDown') && onNext && hasNext) {
        e.preventDefault()
        onNext()
      } else if ((e.key === 'k' || e.key === 'ArrowUp') && onPrev && hasPrev) {
        e.preventDefault()
        onPrev()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [entry, onPrev, onNext, hasPrev, hasNext])

  if (!entry) return null

  const tags = parseTags(entry.tags)
  const scope = scopeOf(entry)
  const invalidated = !!entry.t_valid_end
  const body = stripFrontmatter(entry.content || '')

  async function doInvalidate() {
    if (!onInvalidate) return
    setBusy('invalidate')
    try {
      await onInvalidate(entry!.id)
      setConfirmInvalidate(false)
      onClose()
    } finally {
      setBusy(null)
    }
  }

  async function doDelete() {
    if (!onDelete) return
    setBusy('delete')
    try {
      await onDelete(entry!.id)
      setConfirmDelete(false)
      onClose()
    } finally {
      setBusy(null)
    }
  }

  async function doTogglePin() {
    if (!onTogglePin) return
    setBusy('pin')
    try {
      await onTogglePin(entry!.id, !entry!.pinned)
    } finally {
      setBusy(null)
    }
  }

  async function doUnlinkEntity(row: MemoryEntityRow) {
    if (!entry) return
    const key = `${row.entity_kind}|${row.entity_id}|${row.role}`
    setUnlinkingKey(key)
    try {
      await unlinkMemoryEntity(entry.id, {
        kind: row.entity_kind,
        id: row.entity_id,
        role: row.role,
      })
      setEntities((prev) => prev?.filter((r) => r.id !== row.id) ?? null)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Unlink failed')
    } finally {
      setUnlinkingKey(null)
    }
  }

  return (
    <Sheet open={!!entry} onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 p-0 sm:max-w-[min(760px,94vw)]"
      >
        <SheetHeader className="space-y-3 border-b border-border/60 p-5 pr-12">
          <div className="flex flex-wrap items-center gap-2">
            <KindBadge kind={entry.kind} />
            <ScopeBadge scope={scope} />
            <SourceChip source={entry.source_kind} />
            {entry.pinned && (
              <span className="inline-flex items-center gap-1 border border-amber-500/40 bg-amber-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-amber-300">
                <Pin className="h-3 w-3" /> pinned
              </span>
            )}
            {invalidated && (
              <span className="inline-flex items-center gap-1 border border-muted-foreground/40 bg-muted/40 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">
                invalidated
              </span>
            )}
            <span className="ml-auto text-xs tabular-nums text-muted-foreground">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span>{relativeTime(entry.created_at)}</span>
                </TooltipTrigger>
                <TooltipContent>
                  {new Date(entry.created_at).toLocaleString()}
                </TooltipContent>
              </Tooltip>
            </span>
          </div>
          <SheetTitle className="flex min-w-0 items-start gap-2 font-mono text-base font-semibold leading-snug break-all">
            <span className="min-w-0">{entry.name}</span>
            <CopyButton value={entry.name} className="-mt-0.5 shrink-0" />
          </SheetTitle>
          {tags.length > 0 && (
            <div className="flex flex-wrap items-center gap-1">
              {tags.map((t) => (
                <span
                  key={t}
                  className="inline-flex items-center border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                >
                  {t}
                </span>
              ))}
            </div>
          )}
        </SheetHeader>

        <div className="flex-1 overflow-y-auto px-5 py-4">
          <MemoryActions
            entry={entry}
            busy={busy}
            onTogglePin={onTogglePin ? doTogglePin : undefined}
            onInvalidate={onInvalidate ? () => setConfirmInvalidate(true) : undefined}
            onDelete={onDelete ? () => setConfirmDelete(true) : undefined}
          />

          <Section label="Content">
            {body ? (
              <Markdown source={body} />
            ) : (
              <p className="text-xs italic text-muted-foreground">empty</p>
            )}
          </Section>

          {suggestions && suggestions.length > 0 && (
            <Section label="You might also remember" defaultMuted>
              <ul className="space-y-1.5">
                {suggestions.map((sug) => (
                  <li key={sug.memory_id} className="text-[12px]">
                    <button
                      type="button"
                      onClick={() => {
                        // Defer to the parent's URL-backed selection.
                        // We don't have a setSelected handler here, so
                        // the cheap path is updating the search param
                        // directly — drawer's URL effect re-resolves.
                        const url = new URL(window.location.href)
                        url.searchParams.set('selected', sug.memory_id)
                        window.history.replaceState(null, '', url.toString())
                        window.dispatchEvent(new PopStateEvent('popstate'))
                      }}
                      className="group flex w-full items-center justify-between gap-2 text-left hover:text-primary"
                    >
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span className="font-mono text-[9px] uppercase tracking-wider text-muted-foreground">
                          {sug.source.replace('_', ' ')}
                        </span>
                        <span className="font-mono truncate">{sug.name}</span>
                      </span>
                      <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                        {sug.score.toFixed(2)}
                      </span>
                    </button>
                    <div className="ml-1 text-[10px] text-muted-foreground/70">
                      {sug.reason}
                    </div>
                  </li>
                ))}
              </ul>
            </Section>
          )}

          {entities && entities.length > 0 && (
            <Section label="About">
              <div className="flex flex-wrap items-center gap-1.5">
                {entities.map((row) => {
                  const key = `${row.entity_kind}|${row.entity_id}|${row.role}`
                  const isUnlinking = unlinkingKey === key
                  return (
                    <span
                      key={row.id}
                      className="group inline-flex items-center gap-1 border border-border bg-card/60 pl-1.5 pr-0.5 py-0.5 text-[11px]"
                      title={`role: ${row.role}`}
                    >
                      <Link
                        to={`/memory/about/${encodeURIComponent(row.entity_kind)}/${encodeURIComponent(row.entity_id)}`}
                        className="inline-flex items-center gap-1.5 hover:text-primary"
                      >
                        <span className="font-mono text-[9px] uppercase tracking-wider text-muted-foreground">
                          {row.entity_kind}
                        </span>
                        <span className="font-mono text-foreground">
                          {row.entity_id}
                        </span>
                      </Link>
                      <button
                        type="button"
                        onClick={() => doUnlinkEntity(row)}
                        disabled={isUnlinking}
                        className="ml-0.5 inline-flex h-4 w-4 items-center justify-center text-muted-foreground/50 hover:text-destructive disabled:opacity-30"
                        aria-label={`Unlink ${row.entity_kind}:${row.entity_id}`}
                      >
                        <X className="h-3 w-3" />
                      </button>
                    </span>
                  )
                })}
              </div>
              <p className="mt-2 text-[11px] text-muted-foreground/70">
                Click a chip to see every memory linked to that entity.
              </p>
            </Section>
          )}

          <Section label="Identity" defaultMuted>
            <KV label="ID">
              <code className="font-mono text-xs text-muted-foreground break-all">
                {entry.id}
              </code>
              <CopyButton value={entry.id} />
            </KV>
            {entry.workspace_id && (
              <KV label="Workspace">
                <code className="font-mono text-xs text-foreground break-all">
                  {entry.workspace_id}
                </code>
              </KV>
            )}
            {entry.user_id && (
              <KV label="User">
                <code className="font-mono text-xs text-foreground break-all">
                  {entry.user_id}
                </code>
              </KV>
            )}
          </Section>

          <Section label="Lifecycle">
            <KV label="Created">
              <span className="text-xs text-foreground">
                {new Date(entry.created_at).toLocaleString()}
              </span>
            </KV>
            <KV label="Updated">
              <span className="text-xs text-foreground">
                {new Date(entry.updated_at).toLocaleString()}
              </span>
            </KV>
            <KV label="Valid from">
              <span className="text-xs text-foreground">
                {new Date(entry.t_valid_start).toLocaleString()}
              </span>
            </KV>
            {entry.t_valid_end && (
              <KV label="Invalidated">
                <span className="text-xs text-amber-300">
                  {new Date(entry.t_valid_end).toLocaleString()}
                </span>
              </KV>
            )}
            {entry.invalidated_by && (
              <KV label="Superseded by">
                <code className="font-mono text-xs text-foreground break-all">
                  {entry.invalidated_by}
                </code>
                <CopyButton value={entry.invalidated_by} />
              </KV>
            )}
          </Section>

          <ProvenanceSection
            entry={entry}
            open={showProvenance}
            onToggle={() => setShowProvenance((v) => !v)}
          />
        </div>

        <DrawerFooterNav
          onPrev={onPrev}
          onNext={onNext}
          hasPrev={hasPrev}
          hasNext={hasNext}
        />
      </SheetContent>

      <ConfirmDialog
        open={confirmInvalidate}
        onOpenChange={setConfirmInvalidate}
        title="Invalidate this memory?"
        description="It will be hidden from agents and search results by default, but the row stays in the bi-temporal trail. Use forget-by-source if you need a hard delete."
        confirmLabel="Invalidate"
        onConfirm={doInvalidate}
        variant="default"
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Soft-delete this memory?"
        description="Sets deleted_at, hiding it from all surfaces. The row is retained for the bi-temporal trail."
        confirmLabel="Delete"
        onConfirm={doDelete}
        variant="destructive"
      />
    </Sheet>
  )
}
