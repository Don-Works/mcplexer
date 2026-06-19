// TasksListWidgets — supporting components for the tasks list page.
// Kept in their own file so TasksListPage stays under the 300-line
// guideline + so the components are reusable from a future tree/kanban
// view without circular imports.

import { Fragment, type ReactNode } from 'react'
import { CheckCircle2, ListTree, Loader2, Rows3, Trash2, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export type SortMode = 'updated' | 'created' | 'due' | 'priority' | 'status'
export type ViewMode = 'flat' | 'tree'

// HighlightedText wraps every case-insensitive occurrence of `query`
// in <mark> so the operator's eye lands on the matched word. Empty
// query short-circuits to the plain string so the common no-search
// path stays free.
export function HighlightedText({
  text,
  query,
  className,
}: {
  text: string
  query: string
  className?: string
}) {
  if (!query || !text) return <>{text}</>
  const tokens = query
    .split(/\s+/)
    .map((t) => t.trim())
    .filter((t) => t.length >= 2)
  if (tokens.length === 0) return <>{text}</>
  const splitter = new RegExp(`(${tokens.map(escapeRegex).join('|')})`, 'gi')
  const tester = new RegExp(`^(?:${tokens.map(escapeRegex).join('|')})$`, 'i')
  const parts = text.split(splitter)
  return (
    <span className={className}>
      {parts.map((part, i) =>
        tester.test(part) ? (
          <mark key={i} className="bg-primary/20 px-0.5 text-foreground">
            {part}
          </mark>
        ) : (
          <Fragment key={i}>{part}</Fragment>
        ),
      )}
    </span>
  )
}

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// ViewModeToggle — Flat vs Tree. Tree only makes sense once epics with
// children exist; we still surface both modes so an operator who
// expects the tree affordance always sees it.
export function ViewModeToggle({
  value,
  onChange,
}: {
  value: ViewMode
  onChange: (v: ViewMode) => void
}) {
  return (
    <div className="inline-flex border border-border">
      <button
        onClick={() => onChange('flat')}
        title="Flat list — one row per task"
        className={cn(
          'inline-flex items-center gap-1 border-r border-border px-2 py-1 text-xs',
          value === 'flat'
            ? 'bg-accent text-accent-foreground'
            : 'bg-transparent text-muted-foreground hover:bg-muted/40 hover:text-foreground',
        )}
      >
        <Rows3 className="h-3 w-3" />
        Flat
      </button>
      <button
        onClick={() => onChange('tree')}
        title="Tree — children indent under their parent epic"
        className={cn(
          'inline-flex items-center gap-1 px-2 py-1 text-xs',
          value === 'tree'
            ? 'bg-accent text-accent-foreground'
            : 'bg-transparent text-muted-foreground hover:bg-muted/40 hover:text-foreground',
        )}
      >
        <ListTree className="h-3 w-3" />
        Tree
      </button>
    </div>
  )
}

// BulkActionBar — sticky floating bar at the bottom of the viewport
// when one or more tasks are selected. Lets the operator close or
// delete many at once instead of clicking through each detail page.
export function BulkActionBar({
  count,
  busy,
  onClear,
  onClose,
  onDelete,
}: {
  count: number
  busy: boolean
  onClear: () => void
  onClose: () => void
  onDelete: () => void
}) {
  return (
    <div className="fixed inset-x-0 bottom-4 z-30 flex justify-center px-3">
      <div className="flex items-center gap-2 border border-border bg-card px-3 py-2 shadow-[0_4px_24px_-12px_rgba(0,0,0,0.6)]">
        <span className="text-xs text-muted-foreground">
          <span className="font-mono text-foreground">{count}</span> selected
        </span>
        <span className="mx-1 text-border">·</span>
        <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>
          {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
          Close
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={onDelete}
          disabled={busy}
          className="text-destructive hover:text-destructive"
        >
          <Trash2 className="h-3.5 w-3.5" />
          Delete
        </Button>
        <span className="mx-1 text-border">·</span>
        <Button variant="ghost" size="sm" onClick={onClear} disabled={busy}>
          <X className="h-3.5 w-3.5" />
          Clear
        </Button>
      </div>
    </div>
  )
}

// TaskCreateHint — the empty-state body. Replaces the previous
// "agents create tasks via the task__create MCP tool" prose with an
// actual copy-pasteable invocation an agent (or human reading along)
// can use right now.
export function TaskCreateHint(): ReactNode {
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        Tasks are emitted by agents (or you) via the gateway. The minimal call from any agent connected to MCPlexer:
      </p>
      <pre className="mx-auto inline-block overflow-x-auto border border-border bg-muted/40 px-3 py-2 text-left font-mono text-[12px] leading-relaxed text-foreground/90">
        <code>{`task__create({\n  title: "Patch the audit redaction for peer IDs",\n  priority: "high",\n  tags: ["security"]\n})`}</code>
      </pre>
      <p className="text-xs text-muted-foreground/70">
        Or hit the New task button — same code path, just a form.
      </p>
    </div>
  )
}

// toggleSetMember — pure helper so the page's setSelected handler
// stays a one-liner. Keeping the set logic out of the page makes it
// trivial to test in isolation later.
// eslint-disable-next-line react-refresh/only-export-components
export function toggleSetMember<T>(prev: Set<T>, member: T): Set<T> {
  const next = new Set(prev)
  if (next.has(member)) next.delete(member)
  else next.add(member)
  return next
}
