// WorkerEditorToolsCard.tsx — checkbox grid backed by /api/v1/tools.
//
// Replaces the M0 JSON-textarea fallback for the worker's tool
// allowlist. The grid is grouped by tool namespace; each row carries a
// "write" pill when the tool's name matches the runner-dispatcher's
// write-class heuristic so the operator can see at a glance which
// tools are gated by propose-first mode.
//
// Bulk actions: "Select all", "Select read-only", "Clear", plus
// per-namespace "Select all / Clear". Search filters the visible grid.
// The selection-summary line shows total + write-class count so the
// operator sees the safety surface at a glance before saving.
//
// The JSON textarea is preserved under an <details> for advanced /
// scripting use — it stays the wire format on the Worker row, the
// grid just renders + mutates it.

import { useMemo, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Textarea } from '@/components/ui/textarea'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { ToolCatalogItem } from '@/api/workers'
import type { EditorState } from './worker-editor-state'

type Setter = <K extends keyof EditorState>(key: K, value: EditorState[K]) => void

interface ToolsCardProps {
  state: EditorState
  set: Setter
  tools: ToolCatalogItem[] | null
}

export function ToolsCard({ state, set, tools }: ToolsCardProps) {
  const [query, setQuery] = useState('')
  const selected = useMemo(
    () => parseAllowlist(state.toolAllowlistJSON),
    [state.toolAllowlistJSON],
  )
  const allTools = tools ?? []
  const filtered = useMemo(() => filterTools(allTools, query), [allTools, query])
  const groups = useMemo(() => groupByNamespace(filtered), [filtered])

  const setAllowlist = (names: Set<string>) => {
    set('toolAllowlistJSON', JSON.stringify([...names].sort()))
  }

  const toggle = (name: string) => {
    const next = new Set(selected)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    setAllowlist(next)
  }

  // Top-level bulk actions follow the active filter — "Select all" with
  // a query of "github" means "select every visible github tool", not
  // every tool the daemon knows about. Clear stays global (people use
  // it to start over).
  const selectAll = () =>
    setAllowlist(unionSelected(selected, filtered.map((t) => t.name)))
  const selectReadOnly = () =>
    setAllowlist(
      unionSelected(selected, filtered.filter((t) => !t.write_class).map((t) => t.name)),
    )
  const clearAll = () => setAllowlist(new Set())
  const clearFiltered = () =>
    setAllowlist(diffSelected(selected, filtered.map((t) => t.name)))
  const selectNamespace = (names: string[]) =>
    setAllowlist(unionSelected(selected, names))
  const selectNamespaceReadOnly = (items: ToolCatalogItem[]) =>
    setAllowlist(unionSelected(selected, items.filter((t) => !t.write_class).map((t) => t.name)))
  const clearNamespace = (names: string[]) =>
    setAllowlist(diffSelected(selected, names))

  const hasTools = allTools.length > 0
  const trimmed = state.toolAllowlistJSON.trim()
  const isEmptyAllowlist = trimmed === '' || trimmed === '[]'
  const writeCount = useMemo(
    () => allTools.filter((t) => selected.has(t.name) && t.write_class).length,
    [allTools, selected],
  )
  // Counts pulled from the FILTERED set so the buttons read live as the
  // user types — "Select all (12)" becomes "Select all (3)" when the
  // filter narrows the visible grid.
  const filteredTotal = filtered.length
  const filteredReadOnly = useMemo(
    () => filtered.filter((t) => !t.write_class).length,
    [filtered],
  )
  const isFiltered = query.trim().length > 0

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Tools</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {!hasTools && (
          <p className="text-xs text-muted-foreground">
            No downstream tools registered. Add a server in <strong>Config</strong>{' '}
            to expose tools to workers. An empty allowlist means the worker
            may not call any tool — fail-closed.
          </p>
        )}
        {hasTools && (
          <div className="space-y-3">
            <SelectionSummary
              total={allTools.length}
              selected={selected.size}
              writeCount={writeCount}
              isEmpty={isEmptyAllowlist}
            />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Filter tools by name or description…"
              className="h-8 text-xs"
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" size="sm" variant="outline" className="h-7 px-2 text-xs" onClick={selectAll} disabled={filteredTotal === 0}>
                {isFiltered ? `Select all visible (${filteredTotal})` : `Select all (${filteredTotal})`}
              </Button>
              <Button type="button" size="sm" variant="outline" className="h-7 px-2 text-xs" onClick={selectReadOnly} disabled={filteredReadOnly === 0}>
                {isFiltered ? `Select read-only visible (${filteredReadOnly})` : `Select read-only (${filteredReadOnly})`}
              </Button>
              {isFiltered && (
                <Button type="button" size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={clearFiltered}>
                  Deselect visible
                </Button>
              )}
              <Button type="button" size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={clearAll} disabled={selected.size === 0}>
                Clear all
              </Button>
            </div>
            {groups.length === 0 && query.length > 0 && (
              <p className="text-[11px] text-muted-foreground">
                No tools match <code className="font-mono">{query}</code>.
              </p>
            )}
            {groups.map((g) => (
              <ToolGroupBlock
                key={g.namespace}
                group={g}
                selected={selected}
                onToggle={toggle}
                onSelectAll={() => selectNamespace(g.items.map((t) => t.name))}
                onSelectReadOnly={() => selectNamespaceReadOnly(g.items)}
                onClear={() => clearNamespace(g.items.map((t) => t.name))}
              />
            ))}
          </div>
        )}
        {!hasTools && (
          <details className="text-xs">
            <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
              Allowlist JSON (advanced)
            </summary>
            <Textarea
              rows={3}
              value={state.toolAllowlistJSON}
              onChange={(e) => set('toolAllowlistJSON', e.target.value)}
              placeholder='["mesh__send", "github__list_issues"]'
              className="font-mono text-xs mt-1.5"
            />
            <p className="mt-1 text-[10px] text-muted-foreground/70">
              Use <code>namespace__tool</code> qualified names; <code>[]</code>{' '}
              means deny-everything (fail-closed); <code>null</code> means
              pass-through.
            </p>
          </details>
        )}
      </CardContent>
    </Card>
  )
}

interface SelectionSummaryProps {
  total: number
  selected: number
  writeCount: number
  isEmpty: boolean
}

function SelectionSummary({ total, selected, writeCount, isEmpty }: SelectionSummaryProps) {
  if (isEmpty) {
    return (
      <p className="rounded bg-amber-500/10 px-2 py-1.5 text-[11px] text-amber-700 dark:text-amber-300">
        ⚠ Allowlist empty — worker is blocked from every tool. Use the buttons below to grant access.
      </p>
    )
  }
  return (
    <p className="text-[11px] text-muted-foreground">
      <span className="font-semibold text-foreground">{selected}</span> of {total} tools selected
      {writeCount > 0 && (
        <>
          {' '}· <span className="font-medium text-amber-700 dark:text-amber-400">{writeCount} write-class</span> (gated by propose-mode)
        </>
      )}
    </p>
  )
}

interface ToolGroupBlockProps {
  group: ToolGroup
  selected: Set<string>
  onToggle: (name: string) => void
  onSelectAll: () => void
  onSelectReadOnly: () => void
  onClear: () => void
}

function ToolGroupBlock({ group, selected, onToggle, onSelectAll, onSelectReadOnly, onClear }: ToolGroupBlockProps) {
  const inGroupSelected = group.items.filter((t) => selected.has(t.name)).length
  const readOnlyCount = group.items.filter((t) => !t.write_class).length
  const writeCount = group.items.length - readOnlyCount
  const allSelected = inGroupSelected === group.items.length && group.items.length > 0
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          {group.namespace || 'global'}
          <span className="ml-1.5 text-muted-foreground/60 normal-case">
            ({inGroupSelected}/{group.items.length}
            {writeCount > 0 ? ` · ${writeCount} write` : ''})
          </span>
        </div>
        <div className="flex items-center gap-2 text-[10px] uppercase tracking-wide">
          <button
            type="button"
            onClick={onSelectAll}
            disabled={allSelected}
            className="text-muted-foreground/70 hover:text-foreground disabled:opacity-40 disabled:cursor-default"
          >
            All
          </button>
          <span className="text-muted-foreground/30">·</span>
          <button
            type="button"
            onClick={onSelectReadOnly}
            disabled={readOnlyCount === 0}
            className="text-muted-foreground/70 hover:text-foreground disabled:opacity-40 disabled:cursor-default"
          >
            Read ({readOnlyCount})
          </button>
          <span className="text-muted-foreground/30">·</span>
          <button
            type="button"
            onClick={onClear}
            disabled={inGroupSelected === 0}
            className="text-muted-foreground/70 hover:text-foreground disabled:opacity-40 disabled:cursor-default"
          >
            Clear
          </button>
        </div>
      </div>
      <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
        {group.items.map((t) => (
          <label
            key={t.name}
            className="flex items-start gap-2 rounded border border-border/60 bg-card/40 px-2 py-1.5 text-xs cursor-pointer hover:bg-card"
          >
            <Checkbox
              checked={selected.has(t.name)}
              onCheckedChange={() => onToggle(t.name)}
              aria-label={t.name}
              className="mt-0.5"
            />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5 font-mono text-[11px]">
                <span className="truncate">{t.name}</span>
                {t.write_class && (
                  <Badge
                    variant="outline"
                    className="px-1 py-0 text-[9px] uppercase border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400"
                    title="Write-class — gated by propose-mode unless pre-approved"
                  >
                    write
                  </Badge>
                )}
              </div>
              {t.description && (
                <div className="mt-0.5 text-[10px] text-muted-foreground line-clamp-2">
                  {t.description}
                </div>
              )}
            </div>
          </label>
        ))}
      </div>
    </div>
  )
}

function filterTools(tools: ToolCatalogItem[], query: string): ToolCatalogItem[] {
  const q = query.trim().toLowerCase()
  if (!q) return tools
  return tools.filter(
    (t) =>
      t.name.toLowerCase().includes(q) ||
      (t.description ?? '').toLowerCase().includes(q),
  )
}

function unionSelected(current: Set<string>, additions: string[]): Set<string> {
  const next = new Set(current)
  for (const n of additions) next.add(n)
  return next
}

function diffSelected(current: Set<string>, removals: string[]): Set<string> {
  const next = new Set(current)
  for (const n of removals) next.delete(n)
  return next
}

// parseAllowlist normalises the JSON-string state into a Set so the
// checkbox grid can render checked-state without re-parsing per row.
// Unparseable / non-array input returns an empty set — the operator
// sees zero ticks and an "allowlist is empty" warning above.
function parseAllowlist(raw: string): Set<string> {
  const trimmed = raw.trim()
  if (trimmed === '' || trimmed === 'null') return new Set()
  try {
    const parsed = JSON.parse(trimmed)
    if (!Array.isArray(parsed)) return new Set()
    return new Set(parsed.filter((v): v is string => typeof v === 'string'))
  } catch {
    return new Set()
  }
}

interface ToolGroup {
  namespace: string
  items: ToolCatalogItem[]
}

// groupByNamespace clusters tools so the checkbox grid mirrors the
// gateway's namespacing. Tools without an explicit namespace land in
// the empty bucket which renders as "global".
function groupByNamespace(tools: ToolCatalogItem[]): ToolGroup[] {
  const map = new Map<string, ToolCatalogItem[]>()
  for (const t of tools) {
    const key = t.namespace ?? ''
    const arr = map.get(key) ?? []
    arr.push(t)
    map.set(key, arr)
  }
  return [...map.entries()]
    .map(([namespace, items]) => ({
      namespace,
      items: items.sort((a, b) => a.name.localeCompare(b.name)),
    }))
    .sort((a, b) => a.namespace.localeCompare(b.namespace))
}
