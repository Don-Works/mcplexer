import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Search } from 'lucide-react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { cn } from '@/lib/utils'
import type { CommandEntry, CommandGroup } from './commands'
import { brainCmdEntries, useCommandEntries } from './commands'
import { IndexTypeahead } from '@/pages/brain/components/IndexTypeahead'
import { detectMode, type TypeaheadOption } from '@/pages/brain/components/typeaheadRank'
import { useAuditSearchMode } from './AuditSearchMode'

// Top-level pages surfaced as "Quick Actions" on empty query. The full
// catalog is still searchable; these are the 6 destinations operators
// actually jump to, keeping the default view calm.
const QUICK_ACTION_IDS = [
  'action-create-task',
  'page-dashboard',
  'page-tasks',
  'page-brain',
  'page-workers',
  'page-audit',
  'page-skills',
]

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
}

// CommandPalette — terminal-native cmd+K surface.
//
// Design intent (per /impeccable):
//   • Terminal aesthetic: mono, sharp corners, no gradients, no glass.
//   • Minimal `mcp›` prefix — branding without stealing input width.
//   • Empty-query view is calm: Recent + Quick Actions + Everything.
//     Mode chips (`>` `/` `[[` `#` `@`) teach the grammar interactively.
//   • Matched characters bold + primary-tinted so you see why a result
//     ranked where it did.
//   • Status bar teaches shortcuts while you use them.
//   • Recents remembered in localStorage, surfaced at top.
//   • Dynamic entries (servers/routes/etc.) refresh on every open.

const RECENT_KEY = 'mcplexer.cmdk.recent'
const RECENT_MAX = 8

export function CommandPalette({ open, onOpenChange }: Props) {
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement | null>(null)
  const listRef = useRef<HTMLDivElement | null>(null)
  const [query, setQuery] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  // activeDesc mirrors the inline IndexTypeahead's highlighted option id so the
  // cmd+K input can carry aria-activedescendant in ref/tag/ws (typeahead) mode —
  // the §7 combobox contract the token inputs already honour.
  const [activeDesc, setActiveDesc] = useState('')
  const listboxId = useId()

  const { groups, loading } = useCommandEntries(open)
  const recents = useRecents(open)

  // Inject a Recent group at the top of the static menu (empty query
  // only). Dynamic fetch happens in parallel so this never blocks.
  const groupsWithRecent = useMemo(() => {
    if (recents.length === 0) return groups
    // Resolve recent IDs against the live entry catalog.
    const all = groups.flatMap((g) => g.entries)
    const recentEntries = recents
      .map((id) => all.find((e) => e.id === id))
      .filter((e): e is CommandEntry => !!e)
    if (recentEntries.length === 0) return groups
    return [
      { id: 'recent', label: 'Recent', entries: recentEntries },
      ...groups,
    ]
  }, [groups, recents])

  // Mode grammar (DESIGN §4.0): a leading mono prefix selects the surface mode.
  //   (none) = filter (fuzzy-jump, the existing behavior)
  //   >      = cmd    (brain command verbs)
  //   [[     = ref    (record-ref typeahead — shared IndexTypeahead)
  //   #      = tag    (tag typeahead — shared IndexTypeahead)
  //   @      = workspace (workspace typeahead — shared IndexTypeahead)
  //   /      = audit  (audit-log semantic search — owns its own body + keys)
  // Audit mode is detected locally (not in the shared detectMode grammar)
  // because it is cmd+K-only — the in-field brain pickers never need it.
  const auditMode = query.startsWith('/')
  const auditQuery = auditMode ? query.slice(1) : ''
  const parsedMode = useMemo(() => (auditMode ? null : detectMode(query)), [auditMode, query])
  const taMode =
    parsedMode && (parsedMode.mode === 'ref' || parsedMode.mode === 'tag' || parsedMode.mode === 'workspace')
      ? parsedMode.mode
      : null

  // In cmd mode the flat list is the brain verbs filtered by the residual text;
  // otherwise it is the normal page/server catalog (or empty while a typeahead
  // mode owns the body).
  const flat = useMemo(() => {
    if (auditMode) return []
    if (parsedMode?.mode === 'cmd') {
      const verbs: CommandGroup[] = [{ id: 'brain', label: 'Brain', entries: brainCmdEntries() }]
      return flattenGroups(verbs, parsedMode.text)
    }
    if (taMode) return []
    return flattenGroups(groupsWithRecent, query)
  }, [auditMode, parsedMode, taMode, groupsWithRecent, query])

  // Audit-search mode owns its own body, debounce, and arrow/Enter keys. The
  // hook is always called (rules of hooks); it no-ops while auditMode is off.
  const audit = useAuditSearchMode({
    query: auditQuery,
    navigate,
    onClose: () => onOpenChange(false),
    listboxId,
    onActiveDescendant: setActiveDesc,
  })

  useEffect(() => {
    if (open) {
      setQuery('')
      setActiveIndex(0)
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }, [open])

  useEffect(() => {
    setActiveIndex(0)
  }, [query])

  const run = useCallback(
    (entry: CommandEntry) => {
      saveRecent(entry.id)
      if (entry.run) {
        // run() entries that prime the input (setQuery) switch the palette
        // into a typeahead mode and must keep it open; resetActive so the
        // new mode starts at the top. Navigating/side-effecting run()s close.
        let primed = false
        entry.run({
          navigate,
          setQuery: (q: string) => {
            primed = true
            setQuery(q)
            setActiveIndex(0)
            requestAnimationFrame(() => inputRef.current?.focus())
          },
        })
        if (!primed) onOpenChange(false)
        return
      }
      onOpenChange(false)
      if (entry.to) navigate(entry.to)
    },
    [navigate, onOpenChange],
  )

  // onTypeaheadSelect routes a chosen IndexTypeahead option per the active mode:
  // a ref hit deep-links into the Brain record editor; a tag filters the
  // browser; a workspace switches scope; create-on-miss seeds the new-record
  // flow. The IndexTypeahead owns its own arrow/Enter/Escape keys while open.
  const onTypeaheadSelect = useCallback(
    (opt: TypeaheadOption) => {
      onOpenChange(false)
      if (taMode === 'ref') {
        if (opt.create) {
          navigate('/brain/browse?new=task')
          return
        }
        const kind = opt.hit?.kind === 'memory' ? 'memory' : 'tasks'
        const ws = opt.hit?.workspace ? encodeURIComponent(opt.hit.workspace) : '_'
        navigate(`/brain/browse/${ws}/${kind}/${encodeURIComponent(opt.id)}`)
        return
      }
      if (taMode === 'tag') {
        navigate(`/brain/browse?tag=${encodeURIComponent(opt.id)}`)
        return
      }
      // workspace mode: switch scope in the browser.
      navigate(`/brain/browse?ws=${encodeURIComponent(opt.id)}`)
    },
    [taMode, navigate, onOpenChange],
  )

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    // Audit mode owns navigation/Enter inside its own body — forward to it.
    if (auditMode) {
      audit.onKeyDown(e)
      return
    }
    // While a typeahead mode owns the body, the IndexTypeahead handles
    // arrow/Enter/Escape via its own window listener — don't double-handle.
    if (taMode) {
      if (e.key === 'Escape') onOpenChange(false)
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIndex((i) => Math.min(i + 1, flat.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIndex((i) => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const entry = flat[activeIndex]
      if (entry) run(entry)
    } else if (e.key === 'Escape') {
      onOpenChange(false)
    } else if (e.key === 'Home') {
      e.preventDefault()
      setActiveIndex(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      setActiveIndex(Math.max(0, flat.length - 1))
    }
  }

  useEffect(() => {
    if (!listRef.current) return
    const el = listRef.current.querySelector<HTMLElement>(`[data-command-index="${activeIndex}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [activeIndex])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="max-w-xl gap-0 overflow-hidden border-border bg-card p-0 font-mono shadow-2xl shadow-black/40 sm:max-w-2xl"
        aria-describedby={undefined}
      >
        <DialogTitle className="sr-only">Command palette</DialogTitle>

        {/* Prompt header — search-first: a real search affordance instead of a
            cryptic brand prefix, and the divider rule goes electric on focus so
            the field reads as the live, intentional surface it is. The active
            mode is announced in the status bar, not crammed into the prompt. */}
        <div className="group flex items-center gap-2.5 border-b border-border bg-card px-3.5 transition-colors focus-within:border-primary/50">
          <Search
            className="h-4 w-4 shrink-0 text-muted-foreground/50 transition-colors group-focus-within:text-primary"
            aria-hidden
          />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Search everything…"
            autoFocus
            // In a typeahead / audit mode the input IS the combobox over the
            // inline listbox; mirror the token-input wiring (§7) so a screen
            // reader announces the highlighted option as you arrow.
            role={taMode || auditMode ? 'combobox' : undefined}
            aria-expanded={taMode || auditMode ? true : undefined}
            aria-autocomplete={taMode || auditMode ? 'list' : undefined}
            aria-controls={taMode || auditMode ? listboxId : undefined}
            aria-activedescendant={(taMode || auditMode) && activeDesc ? activeDesc : undefined}
            data-testid="cmdk-input"
            className={cn(
              'h-12 min-w-0 flex-1 border-0 bg-transparent p-0 font-mono text-[14px] text-foreground placeholder:text-muted-foreground/40',
              // Kill BOTH the ring and its offset — the global :focus-visible rule
              // in index.css paints a 2px ring-offset in the page background
              // (near-black), which on the lighter card reads as an ugly black box
              // around the field. The focus signal here is the electric bottom rule.
              'outline-none focus:outline-none focus-visible:ring-0 focus-visible:ring-offset-0',
            )}
            spellCheck={false}
            autoCorrect="off"
            autoCapitalize="off"
          />
          <kbd className="hidden shrink-0 border border-border bg-background/40 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground transition-colors group-focus-within:border-primary/30 group-focus-within:text-foreground/80 sm:inline-block">
            esc
          </kbd>
        </div>

        {/* Results — a typeahead mode (ref/tag/@) hands the body to the shared
            IndexTypeahead; cmd / filter modes render the catalog rows. */}
        <div ref={listRef} className="relative max-h-[60vh] min-h-[18rem] overflow-y-auto py-1">
          {auditMode ? (
            audit.body
          ) : taMode ? (
            <IndexTypeahead
              open
              inline
              query={parsedMode?.text ?? ''}
              mode={taMode}
              listboxId={listboxId}
              onActiveDescendant={setActiveDesc}
              onSelect={onTypeaheadSelect}
              onClose={() => onOpenChange(false)}
            />
          ) : flat.length === 0 ? (
            <EmptyResults query={query} loading={loading} onPrimeQuery={setQuery} />
          ) : parsedMode?.mode === 'cmd' ? (
            <div className="px-1">
              {flat.map((entry, i) => (
                <Row
                  key={entry.id}
                  entry={entry}
                  query={parsedMode.text.trim().toLowerCase()}
                  index={i}
                  active={i === activeIndex}
                  onSelect={() => run(entry)}
                />
              ))}
            </div>
          ) : (
            renderResults(groupsWithRecent, query, activeIndex, flat, run)
          )}
        </div>

        {/* Status bar — tmux-style. Teaches the shortcuts while you use them. */}
        <div className="flex items-center justify-between border-t border-border bg-muted/30 px-3 py-1.5 font-mono text-[10px] text-muted-foreground">
          <div className="flex items-center gap-3">
            <ShortcutHint k="↑ ↓" label="navigate" />
            <ShortcutHint k="↵" label="open" />
            <ShortcutHint k="esc" label="close" />
          </div>
          <div className="tabular-nums">
            {loading && !taMode && !auditMode && (
              <span className="mr-2 animate-pulse text-muted-foreground">fetching…</span>
            )}
            {auditMode ? (
              <span className="uppercase tracking-wider text-primary/80">audit</span>
            ) : taMode ? (
              <span className="uppercase tracking-wider text-primary/80">{taMode}</span>
            ) : (
              <span>
                {flat.length} result{flat.length === 1 ? '' : 's'}
              </span>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function ShortcutHint({ k, label }: { k: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <kbd className="border border-border bg-background/40 px-1 py-px font-mono text-[9px] text-foreground/80">
        {k}
      </kbd>
      <span>{label}</span>
    </span>
  )
}

const MODE_CHIPS = [
  { prefix: '>', label: 'cmd', desc: 'brain verbs' },
  { prefix: '/', label: 'audit', desc: 'search logs' },
  { prefix: '[[', label: 'ref', desc: 'jump to record' },
  { prefix: '#', label: 'tag', desc: 'filter by tag' },
  { prefix: '@', label: 'ws', desc: 'switch workspace' },
]

function EmptyResults({
  query,
  loading,
  onPrimeQuery,
}: {
  query: string
  loading: boolean
  onPrimeQuery?: (q: string) => void
}) {
  if (query.trim() === '') {
    return (
      <div className="px-4 py-10 text-center text-[12px] text-muted-foreground">
        {loading ? (
          <span className="animate-pulse">fetching catalog…</span>
        ) : (
          <>
            <p className="text-foreground/80">Type to jump anywhere.</p>
            <div className="mt-3 flex flex-wrap items-center justify-center gap-1.5">
              {MODE_CHIPS.map((m) => (
                <button
                  key={m.label}
                  type="button"
                  onClick={() => onPrimeQuery?.(m.prefix)}
                  className="group inline-flex items-center gap-1 border border-border bg-muted/30 px-2 py-0.5 font-mono text-[11px] text-foreground/70 transition-colors hover:border-primary/40 hover:bg-accent/40 hover:text-foreground"
                  title={`${m.prefix} — ${m.desc}`}
                >
                  <span className="text-primary/70 group-hover:text-primary">{m.prefix}</span>
                  {m.label}
                </button>
              ))}
            </div>
          </>
        )}
      </div>
    )
  }
  return (
    <div className="px-4 py-10 text-center text-[12px] text-muted-foreground">
      no matches for <span className="text-foreground">"{query}"</span>
    </div>
  )
}

function flattenGroups(groups: CommandGroup[], query: string): CommandEntry[] {
  const q = query.trim().toLowerCase()
  const out: CommandEntry[] = []
  const seen = new Set<string>()
  for (const g of groups) {
    for (const e of g.entries) {
      if (seen.has(e.id)) continue // recent + page dup
      if (q === '' || scoreEntry(e, q) > 0) {
        out.push(e)
        seen.add(e.id)
      }
    }
  }
  if (q !== '') {
    out.sort((a, b) => scoreEntry(b, q) - scoreEntry(a, q))
  }
  return out
}

function SectionHeader({ label, count }: { label: string; count: number }) {
  return (
    <div className="flex items-baseline justify-between px-3 pb-1 pt-2.5">
      <span className="text-[11px] font-medium text-muted-foreground/70">{label}</span>
      <span className="font-mono text-[10px] tabular-nums text-muted-foreground/40">{count}</span>
    </div>
  )
}

function renderResults(
  groups: CommandGroup[],
  query: string,
  activeIndex: number,
  flat: CommandEntry[],
  run: (e: CommandEntry) => void,
): React.ReactNode {
  const q = query.trim().toLowerCase()
  if (q !== '') {
    return (
      <div className="px-1">
        {flat.map((entry, i) => (
          <Row
            key={entry.id}
            entry={entry}
            query={q}
            index={i}
            active={i === activeIndex}
            onSelect={() => run(entry)}
          />
        ))}
      </div>
    )
  }
  // Empty query: 3-section layout using flat as the source of truth for
  // index alignment (keyboard nav and rendering must agree on order).
  const recentGroup = groups.find((g) => g.id === 'recent')
  const recentEntries = recentGroup?.entries ?? []
  const quickIds = new Set(QUICK_ACTION_IDS)
  const recentIds = new Set(recentEntries.map((e) => e.id))
  const quickEntries = flat.filter((e) => quickIds.has(e.id) && !recentIds.has(e.id))
  const quickAndRecentIds = new Set([...recentIds, ...quickIds])
  const everythingEntries = flat.filter((e) => !quickAndRecentIds.has(e.id))
  const indexById = new Map(flat.map((e, i) => [e.id, i]))

  function renderSection(label: string, entries: CommandEntry[]) {
    if (entries.length === 0) return null
    return (
      <div className="pb-1">
        <SectionHeader label={label} count={entries.length} />
        <div className="px-1">
          {entries.map((entry) => {
            const idx = indexById.get(entry.id) ?? 0
            return (
              <Row
                key={entry.id}
                entry={entry}
                query=""
                index={idx}
                active={idx === activeIndex}
                onSelect={() => run(entry)}
              />
            )
          })}
        </div>
      </div>
    )
  }

  return (
    <>
      {renderSection('Recent', recentEntries)}
      {renderSection('Quick Actions', quickEntries)}
      {renderSection('Everything', everythingEntries)}
    </>
  )
}

function Row({
  entry,
  query,
  index,
  active,
  onSelect,
}: {
  entry: CommandEntry
  query: string
  index: number
  active: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      data-command-index={index}
      data-testid={`cmdk-row-${entry.id}`}
      onClick={onSelect}
      // Hovering doesn't fight keyboard nav — selection state is owned
      // by the parent; we let Mouseup commit. Keeps the active row
      // predictable when alternating mouse + arrow keys.
      className={cn(
        'group flex w-full items-center gap-3 px-3 py-2 text-left transition-colors',
        active ? 'bg-accent/50' : 'hover:bg-accent/25',
      )}
    >
      {entry.icon ? (
        <span
          className={cn(
            'shrink-0 transition-colors',
            active ? 'text-primary' : 'text-muted-foreground/60',
          )}
          aria-hidden
        >
          {entry.icon}
        </span>
      ) : (
        <span
          className={cn(
            'shrink-0 font-mono text-[11px] opacity-0 transition-opacity',
            active && 'text-primary opacity-100',
          )}
          aria-hidden
        >
          ›
        </span>
      )}

      <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-foreground">
        {query ? <Highlighted text={entry.label} query={query} /> : entry.label}
      </span>

      {entry.statusDot && (
        <span
          aria-hidden
          className={cn('h-1.5 w-1.5 shrink-0', dotTone(entry.statusDot))}
          title={entry.statusDot}
        />
      )}

      {entry.hint && (
        <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60">
          {entry.hint}
        </span>
      )}
    </button>
  )
}

// Highlighted renders a label with matched query characters bold + tinted.
function Highlighted({ text, query }: { text: string; query: string }) {
  const lower = text.toLowerCase()
  const out: React.ReactNode[] = []
  let qi = 0
  let buf = ''
  for (let i = 0; i < text.length; i++) {
    if (qi < query.length && lower[i] === query[qi]) {
      if (buf) {
        out.push(<span key={`p${i}`}>{buf}</span>)
        buf = ''
      }
      out.push(
        <span key={`m${i}`} className="font-semibold text-primary">
          {text[i]}
        </span>,
      )
      qi++
    } else {
      buf += text[i]
    }
  }
  if (buf) out.push(<span key="tail">{buf}</span>)
  return <>{out}</>
}

function dotTone(status: 'ok' | 'warn' | 'err' | 'idle'): string {
  switch (status) {
    case 'ok':
      return 'bg-emerald-500'
    case 'warn':
      return 'bg-amber-500'
    case 'err':
      return 'bg-destructive'
    default:
      return 'bg-muted-foreground/30'
  }
}

// Recents — small localStorage-backed MRU list. Stores entry IDs only;
// resolution against the live catalog happens on every open so stale
// IDs silently drop out.
function useRecents(open: boolean): string[] {
  const [recents, setRecents] = useState<string[]>([])
  useEffect(() => {
    if (!open) return
    try {
      const raw = localStorage.getItem(RECENT_KEY)
      if (!raw) return
      const arr = JSON.parse(raw)
      if (Array.isArray(arr)) setRecents(arr.filter((x): x is string => typeof x === 'string'))
    } catch {
      // ignore
    }
  }, [open])
  return recents
}

function saveRecent(id: string) {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    let arr: string[] = []
    if (raw) {
      const parsed = JSON.parse(raw)
      if (Array.isArray(parsed)) arr = parsed.filter((x): x is string => typeof x === 'string')
    }
    arr = [id, ...arr.filter((x) => x !== id)].slice(0, RECENT_MAX)
    localStorage.setItem(RECENT_KEY, JSON.stringify(arr))
  } catch {
    // localStorage may be unavailable in private mode — silent ignore.
  }
}

// scoreEntry — subsequence scorer used by both render + sort. Each
// query char must appear in order in the label; consecutive matches
// near the start score highest. Keywords are matched too but at a
// slight penalty so name matches win.
function scoreMatch(label: string | undefined, query: string): number {
  if (!label) return 0
  const l = label.toLowerCase()
  let li = 0
  let qi = 0
  let score = 0
  let streak = 0
  while (li < l.length && qi < query.length) {
    if (l[li] === query[qi]) {
      qi++
      streak++
      score += 10 + streak * 2 + (li === 0 ? 5 : 0)
    } else {
      streak = 0
    }
    li++
  }
  return qi === query.length ? score : 0
}

function scoreEntry(e: CommandEntry, q: string): number {
  return Math.max(scoreMatch(e.label, q), scoreMatch(e.keywords, q) * 0.7)
}
