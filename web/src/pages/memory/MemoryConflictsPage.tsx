// MemoryConflictsPage — the "conflicts to review" queue (/memory/conflicts).
// When a note is saved, the neighbour scan flags existing memories that look
// like duplicates or potential conflicts and persists them here (migration
// 116). The operator reviews each pair and records a resolution:
//   • Supersede — invalidate the OLDER note, pointing it at the new one.
//   • Keep both — they're complementary; close the flag.
//   • Dismiss   — not a real conflict.

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { AlertTriangle, ArrowLeft, ArrowRight, Check, Copy, Layers, Loader2 } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { toast } from 'sonner'
import {
  getMemoryConflicts,
  invalidateMemory,
  resolveMemoryConflict,
  type ConflictResolution,
  type MemoryConflict,
} from '@/api/memory'

export function MemoryConflictsPage() {
  const [conflicts, setConflicts] = useState<MemoryConflict[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<Record<string, ConflictResolution | ''>>({})

  const refetch = useCallback(async () => {
    setLoading(true)
    try {
      const out = await getMemoryConflicts(200)
      setConflicts(out.conflicts || [])
    } catch (err) {
      toast.error('Failed to load conflicts: ' + String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refetch()
  }, [refetch])

  const resolve = async (c: MemoryConflict, resolution: ConflictResolution) => {
    setBusy((p) => ({ ...p, [c.id]: resolution }))
    try {
      // Supersede invalidates the EXISTING (candidate) note, pointing it at
      // the newer one that triggered the conflict.
      if (resolution === 'superseded') {
        await invalidateMemory(c.candidate_id, c.memory_id)
      }
      await resolveMemoryConflict(c.id, resolution)
      setConflicts((prev) => prev.filter((x) => x.id !== c.id))
      toast.success(
        resolution === 'superseded'
          ? 'Superseded the older note'
          : resolution === 'kept_both'
            ? 'Kept both notes'
            : 'Dismissed',
      )
    } catch (err) {
      toast.error('Resolve failed: ' + String(err))
    } finally {
      setBusy((p) => ({ ...p, [c.id]: '' }))
    }
  }

  return (
    <div className="space-y-5">
      <Link
        to="/memory"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="h-3 w-3" />
        Memory
      </Link>
      <header className="space-y-1.5">
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <AlertTriangle className="h-5 w-5 text-primary" />
          Conflicts to review
        </h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          When a note is saved, mcplexer flags existing memories that look like
          duplicates or potential conflicts. Nothing is changed automatically —
          review each pair and decide: supersede the older note, keep both, or
          dismiss the flag.
        </p>
      </header>

      {loading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading…
        </div>
      )}

      {!loading && conflicts.length === 0 && (
        <Card>
          <CardContent className="p-8 text-center text-sm text-muted-foreground">
            <Check className="mx-auto mb-2 h-6 w-6 text-emerald-400/80" />
            No conflicts to review. New duplicates or contradictions will appear
            here as memories are saved.
          </CardContent>
        </Card>
      )}

      <div className="space-y-3">
        {conflicts.map((c) => (
          <ConflictCard key={c.id} c={c} busy={busy[c.id] || ''} onResolve={resolve} />
        ))}
      </div>
    </div>
  )
}

function ConflictCard({
  c,
  busy,
  onResolve,
}: {
  c: MemoryConflict
  busy: ConflictResolution | ''
  onResolve: (c: MemoryConflict, r: ConflictResolution) => void
}) {
  const isDup = c.kind === 'duplicate'
  return (
    <Card>
      <CardContent className="space-y-3 p-5">
        <div className="flex items-center gap-2">
          <Badge
            variant="outline"
            tone={isDup ? 'warn' : 'muted'}
            className="text-[10px] uppercase tracking-wider"
          >
            {isDup ? (
              <Copy className="mr-1 h-3 w-3" />
            ) : (
              <Layers className="mr-1 h-3 w-3" />
            )}
            {c.kind}
          </Badge>
          <span className="text-[11px] text-muted-foreground">{c.reason}</span>
        </div>

        <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center">
          <NoteChip label="New" name={c.memory_name} id={c.memory_id} accent />
          <ArrowRight className="mx-auto h-4 w-4 shrink-0 rotate-90 text-muted-foreground sm:rotate-0" />
          <NoteChip
            label="Existing"
            name={c.candidate_name}
            id={c.candidate_id}
            preview={c.candidate_preview}
          />
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-border/40 pt-3">
          <Button size="sm" onClick={() => onResolve(c, 'superseded')} disabled={busy !== ''}>
            {busy === 'superseded' ? (
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
            ) : (
              <Check className="mr-1.5 h-3.5 w-3.5" />
            )}
            Supersede older
          </Button>
          <Button size="sm" variant="ghost" onClick={() => onResolve(c, 'kept_both')} disabled={busy !== ''}>
            Keep both
          </Button>
          <Button size="sm" variant="ghost" onClick={() => onResolve(c, 'dismissed')} disabled={busy !== ''}>
            Dismiss
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function NoteChip({
  label,
  name,
  id,
  preview,
  accent,
}: {
  label: string
  name: string
  id: string
  preview?: string
  accent?: boolean
}) {
  return (
    <Link
      to={`/memory/all?selected=${encodeURIComponent(id)}`}
      className={
        'min-w-0 flex-1 border px-3 py-2 transition-colors hover:border-primary/50 ' +
        (accent ? 'border-primary/40 bg-primary/5' : 'border-border bg-card/40')
      }
    >
      <div className="text-[9px] font-semibold uppercase tracking-wider text-muted-foreground/70">
        {label}
      </div>
      <div className="truncate text-sm font-medium">{name || '(untitled)'}</div>
      {preview && (
        <div className="mt-0.5 line-clamp-2 text-[11px] text-muted-foreground/80">{preview}</div>
      )}
    </Link>
  )
}
