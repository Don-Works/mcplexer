import { useMemo, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { AlertTriangle, FileText } from 'lucide-react'
import type {
  BrainConflictDetail,
  BrainTaskRecord,
  BrainMemoryRecord,
} from '@/api/brainBrowser'

// FieldPick is the per-field reconciliation choice: keep the operator's value
// (mine) or take the on-disk value (theirs).
type FieldPick = 'mine' | 'theirs'

interface FieldRow {
  field: string
  mine: string
  theirs: string
  same: boolean
}

interface Props {
  detail: BrainConflictDetail
  kind: 'task' | 'memory'
  // The operator's in-flight draft (YOURS), as a flat string map for display.
  mine: BrainTaskRecord | BrainMemoryRecord
  open: boolean
  // onResolve receives the merged record + the fresh on_disk_hash to submit as
  // if_hash on the retry, plus a flag for "take all theirs & re-edit" (reload).
  onResolve: (merged: BrainTaskRecord | BrainMemoryRecord, ifHash: string) => void
  onReload: () => void
  onCancel: () => void
}

// flat projects a record to the comparable scalar fields the reconciler diffs.
// Structured frontmatter (status/priority/tags) is field-level; the prose body
// is handled via the raw-.md escape hatch (it does not field-merge).
function flatTask(r?: BrainTaskRecord): Record<string, string> {
  if (!r) return {}
  return {
    title: r.title ?? '',
    status: r.status ?? '',
    priority: r.priority ?? '',
    tags: (r.tags ?? []).map((t) => `#${t}`).join(' '),
    pinned: r.pinned ? 'true' : 'false',
  }
}
function flatMemory(r?: BrainMemoryRecord): Record<string, string> {
  if (!r) return {}
  return {
    name: r.name ?? '',
    kind: r.kind ?? '',
    tags: (r.tags ?? []).map((t) => `#${t}`).join(' '),
    pinned: r.pinned ? 'true' : 'false',
  }
}

export function ConflictReconciler({ detail, kind, mine, open, onResolve, onReload, onCancel }: Props) {
  const theirsRec = kind === 'task' ? detail.on_disk_task : detail.on_disk_memory
  const mineFlat = kind === 'task' ? flatTask(mine as BrainTaskRecord) : flatMemory(mine as BrainMemoryRecord)
  const theirsFlat =
    kind === 'task' ? flatTask(theirsRec as BrainTaskRecord) : flatMemory(theirsRec as BrainMemoryRecord)

  const rows: FieldRow[] = useMemo(() => {
    const keys = Array.from(new Set([...Object.keys(mineFlat), ...Object.keys(theirsFlat)]))
    return keys.map((field) => ({
      field,
      mine: mineFlat[field] ?? '',
      theirs: theirsFlat[field] ?? '',
      same: (mineFlat[field] ?? '') === (theirsFlat[field] ?? ''),
    }))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail, mine])

  // Per-field pick. Defaults to "theirs" for differing fields (the newer
  // on-disk value) so a blind "keep mine" never silently re-clobbers.
  const [picks, setPicks] = useState<Record<string, FieldPick>>(() => {
    const init: Record<string, FieldPick> = {}
    for (const row of rows) init[row.field] = row.same ? 'mine' : 'theirs'
    return init
  })
  const [showRaw, setShowRaw] = useState(false)

  const bodyDiffers = useMemo(() => {
    if (kind === 'task') {
      return (mine as BrainTaskRecord).description !== (theirsRec as BrainTaskRecord | undefined)?.description
    }
    return (mine as BrainMemoryRecord).content !== (theirsRec as BrainMemoryRecord | undefined)?.content
  }, [kind, mine, theirsRec])

  // The prose body is NOT a flat field, so it needs its own explicit pick.
  // Default to "mine": the common conflict is "I edited the description while
  // something else touched the file" — silently taking theirs would drop the
  // operator's draft (a silent LWW, banned by DESIGN §3.6). When the body
  // differs the operator MUST choose before merge can apply.
  const [bodyPick, setBodyPick] = useState<FieldPick>('mine')

  function buildMerged(): BrainTaskRecord | BrainMemoryRecord {
    // Start from theirs (the fresh on-disk record) so server-owned fields are
    // current, then overlay each field the operator chose to keep.
    const base = { ...(theirsRec ?? mine) } as BrainTaskRecord & BrainMemoryRecord
    for (const row of rows) {
      if (picks[row.field] !== 'mine') continue
      applyField(base, row.field, mine)
    }
    // Body is never in the flat map. base starts from theirs, so it already
    // carries the on-disk body; only overlay mine when the operator kept it
    // (or when the body did not diverge — "mine" === "theirs" then anyway).
    if (!bodyDiffers || bodyPick === 'mine') {
      const m = mine as BrainTaskRecord & BrainMemoryRecord
      if (kind === 'task') base.description = m.description
      else base.content = m.content
    }
    return base
  }

  return (
    <>
      <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
        <DialogContent className="max-w-2xl rounded-none">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-base">
              <AlertTriangle className="h-4 w-4 text-amber-300" />
              This record changed on disk while you were editing
              <Badge tone="warn" className="ml-2 rounded-none font-mono text-[10px]">
                writer: {detail.writer}
              </Badge>
            </DialogTitle>
          </DialogHeader>

          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b text-left text-xs text-muted-foreground">
                <th className="py-1.5 pr-3 font-medium">field</th>
                <th className="py-1.5 pr-3 font-medium">YOURS</th>
                <th className="py-1.5 pr-3 font-medium">ON DISK</th>
                <th className="py-1.5 font-medium" />
              </tr>
            </thead>
            <tbody className="divide-y">
              {rows.map((row) => (
                <tr key={row.field}>
                  <td className="py-1.5 pr-3 font-mono text-xs">{row.field}</td>
                  <td className="py-1.5 pr-3 font-mono text-xs">{row.mine || '-'}</td>
                  <td className="py-1.5 pr-3 font-mono text-xs">{row.theirs || '-'}</td>
                  <td className="py-1.5">
                    {row.same ? (
                      <span className="text-[10px] text-muted-foreground">(same)</span>
                    ) : (
                      <div className="flex gap-1">
                        <button
                          type="button"
                          onClick={() => setPicks((p) => ({ ...p, [row.field]: 'mine' }))}
                          className={pickClass(picks[row.field] === 'mine')}
                        >
                          keep mine
                        </button>
                        <button
                          type="button"
                          onClick={() => setPicks((p) => ({ ...p, [row.field]: 'theirs' }))}
                          className={pickClass(picks[row.field] === 'theirs')}
                        >
                          take theirs
                        </button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>

          {bodyDiffers && (
            <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-2 text-xs text-muted-foreground">
              <span>
                {kind === 'task' ? 'Description' : 'Content'} (prose) differs - choose which body the merge keeps.
              </span>
              <div className="flex items-center gap-2">
                <div className="flex gap-1">
                  <button
                    type="button"
                    onClick={() => setBodyPick('mine')}
                    className={pickClass(bodyPick === 'mine')}
                  >
                    keep mine
                  </button>
                  <button
                    type="button"
                    onClick={() => setBodyPick('theirs')}
                    className={pickClass(bodyPick === 'theirs')}
                  >
                    take theirs
                  </button>
                </div>
                <button
                  type="button"
                  onClick={() => setShowRaw(true)}
                  className="flex items-center gap-1 text-primary hover:underline"
                >
                  <FileText className="h-3 w-3" /> open both in file view
                </button>
              </div>
            </div>
          )}

          <DialogFooter className="gap-2">
            <Button variant="outline" size="sm" className="rounded-none" onClick={onReload}>
              take all theirs &amp; re-edit
            </Button>
            <Button
              size="sm"
              className="rounded-none"
              onClick={() => onResolve(buildMerged(), detail.on_disk_hash)}
            >
              apply merge
            </Button>
            <Button variant="ghost" size="sm" className="rounded-none" onClick={onCancel}>
              cancel
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Sheet open={showRaw} onOpenChange={setShowRaw}>
        <SheetContent side="right" className="w-[640px] max-w-[90vw] rounded-none sm:max-w-[640px]">
          <SheetHeader>
            <SheetTitle className="font-mono text-xs">{detail.path}</SheetTitle>
          </SheetHeader>
          <div className="grid h-full grid-rows-2 gap-2 overflow-hidden pt-3">
            <RawPane label="YOURS (your draft body)" text={bodyOf(kind, mine)} />
            <RawPane label="ON DISK (their body)" text={bodyOf(kind, theirsRec)} />
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}

function RawPane({ label, text }: { label: string; text: string }) {
  return (
    <div className="flex flex-col overflow-hidden">
      <div className="mb-1 text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <pre className="flex-1 overflow-auto whitespace-pre-wrap border bg-muted/30 p-2 font-mono text-xs">
        {text || '(empty)'}
      </pre>
    </div>
  )
}

function bodyOf(kind: 'task' | 'memory', r?: BrainTaskRecord | BrainMemoryRecord): string {
  if (!r) return ''
  return kind === 'task' ? (r as BrainTaskRecord).description ?? '' : (r as BrainMemoryRecord).content ?? ''
}

// applyField overlays one operator-kept field from `mine` onto the merged
// base. Field names are disjoint across task/memory so no kind discriminator
// is needed.
function applyField(
  base: BrainTaskRecord & BrainMemoryRecord,
  field: string,
  mine: BrainTaskRecord | BrainMemoryRecord,
) {
  const m = mine as BrainTaskRecord & BrainMemoryRecord
  switch (field) {
    case 'title':
      base.title = m.title
      break
    case 'status':
      base.status = m.status
      break
    case 'priority':
      base.priority = m.priority
      break
    case 'name':
      base.name = m.name
      break
    case 'kind':
      base.kind = m.kind
      break
    case 'tags':
      base.tags = m.tags
      break
    case 'pinned':
      base.pinned = m.pinned
      break
  }
}

function pickClass(active: boolean): string {
  return (
    'rounded-none border px-1.5 py-0.5 text-[10px] transition-colors ' +
    (active ? 'border-primary bg-primary/10 text-primary' : 'border-input text-muted-foreground hover:bg-muted/60')
  )
}
