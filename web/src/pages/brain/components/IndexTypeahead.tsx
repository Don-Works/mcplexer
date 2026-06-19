import { useEffect, useId, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { searchBrain } from '@/api/brainBrowser'
import { rankRefOptions, rankTagOptions, type TypeaheadMode, type TypeaheadOption } from './typeaheadRank'

// IndexTypeahead — the ONE shared dropdown + ranking engine (DESIGN §4.0/§4.1).
// Backed by GET /api/v1/brain/search (frecency, exact-prefix -> token -> fuzzy,
// ~10k scale-cliff fallback handled server-side). Renders an ARIA listbox with
// arrow-nav and a create-on-miss row that is ALWAYS last. The same component
// powers RefTokenInput, TagTokenInput, and the cmd+K CommandSurface so the
// operator learns one grammar. This is the dropdown body only — the parent owns
// the text input + the committed-token chips.
interface Props {
  // query is the live typed text (already stripped of any mode prefix).
  query: string
  // mode selects ref / tag / workspace ranking (DESIGN §4.0 grammar).
  mode: TypeaheadMode
  // workspace scopes the search to the active workspace's fusion (optional).
  workspace?: string
  // onSelect commits a chosen option (a real hit or the create-on-miss row).
  onSelect: (opt: TypeaheadOption) => void
  // onClose lets the parent dismiss on Escape.
  onClose?: () => void
  // open gates rendering — the parent shows it only while the field is active.
  open: boolean
  // listboxId lets the parent input wire aria-controls/activedescendant.
  listboxId?: string
  // onActiveDescendant reports the DOM id of the currently-highlighted option
  // (or "" when none) so the owning combobox input can set
  // aria-activedescendant — the load-bearing typeahead a11y wiring (DESIGN §7).
  onActiveDescendant?: (id: string) => void
  // inline renders the listbox in normal flow (the cmd+K body) instead of as an
  // absolutely-positioned in-field popover (token inputs).
  inline?: boolean
}

// useTypeaheadOptions debounces the query, calls /brain/search, and maps the
// hits onto the rendered option list (ref vs tag ranking) + the create-on-miss
// row. Workspace search degrades to a ref search filtered client-side; the
// index has no separate workspace search endpoint and the set is tiny.
function useTypeaheadOptions(query: string, mode: TypeaheadMode, workspace?: string) {
  const [options, setOptions] = useState<TypeaheadOption[]>([])
  const [loading, setLoading] = useState(false)
  useEffect(() => {
    let cancelled = false
    const handle = window.setTimeout(() => {
      // Tag/ref/workspace all search records; tags are mined from the returned
      // hits, workspaces filtered client-side. No kind narrowing — the shared
      // engine returns both task + memory hits.
      setLoading(true)
      searchBrain(query, { workspace, limit: 20 })
        .then((res) => {
          if (cancelled) return
          setOptions(
            mode === 'tag'
              ? rankTagOptions(res.hits, query)
              : rankRefOptions(res.hits, query),
          )
        })
        .catch(() => {
          if (cancelled) return
          // Search failed mid-keystroke: still offer create-on-miss so the
          // writing flow never breaks (DESIGN §4.1 create-on-miss is the
          // killer behavior).
          const q = query.trim()
          setOptions(
            q
              ? [
                  mode === 'tag'
                    ? { id: q, label: `Create #${q}`, sub: 'new tag', create: true }
                    : { id: q, label: `Create "${q}" as a new record`, sub: 'create', create: true },
                ]
              : [],
          )
        })
        .finally(() => {
          if (!cancelled) setLoading(false)
        })
    }, 120)
    return () => {
      cancelled = true
      window.clearTimeout(handle)
    }
  }, [query, mode, workspace])
  return { options, loading }
}

export function IndexTypeahead({ query, mode, workspace, onSelect, onClose, open, listboxId, inline, onActiveDescendant }: Props) {
  const fallbackId = useId()
  const id = listboxId ?? fallbackId
  const { options, loading } = useTypeaheadOptions(query, mode, workspace)
  // active is the raw highlight cursor; safeActive clamps it to the live option
  // count at render time so the highlight never points past the end without an
  // option-set-reset effect (avoids the set-state-in-effect anti-pattern).
  const [active, setActive] = useState(0)
  const safeActive = options.length === 0 ? 0 : Math.min(active, options.length - 1)
  const listRef = useRef<HTMLUListElement | null>(null)

  // optId is the deterministic DOM id of one option row, shared by the rendered
  // <li> and the aria-activedescendant the owning combobox points at.
  const optId = (i: number) => `${id}-opt-${i}`

  // Report the active option id to the owning input for aria-activedescendant.
  // Empty when closed or no options so the attribute is dropped (never points
  // at a non-existent node — a worse a11y bug than no pointer at all).
  useEffect(() => {
    onActiveDescendant?.(open && options.length > 0 ? optId(safeActive) : '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, options.length, safeActive])

  // Keyboard nav is owned here while open: arrows move, Enter selects, Escape
  // closes. Arrows clamp against the live option count; Enter reads the clamped
  // cursor so a shrunk list never selects a stale row.
  useEffect(() => {
    if (!open) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(i + 1, options.length - 1))
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(i - 1, 0))
      } else if (e.key === 'Enter') {
        const cur = options.length === 0 ? 0 : Math.min(active, options.length - 1)
        if (options[cur]) {
          e.preventDefault()
          onSelect(options[cur])
        }
      } else if (e.key === 'Escape') {
        e.preventDefault()
        onClose?.()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [open, options, active, onSelect, onClose])

  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-ta-index="${safeActive}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [safeActive])

  if (!open) return null

  return (
    <ul
      ref={listRef}
      id={id}
      role="listbox"
      aria-label="Index typeahead results"
      className={cn(
        'font-mono text-xs',
        inline
          ? 'px-1'
          : 'absolute left-0 right-0 top-full z-50 mt-1 max-h-64 overflow-y-auto border border-border bg-card shadow-2xl shadow-black/40',
      )}
    >
      {options.length === 0 && (
        <li className="px-3 py-2 text-muted-foreground/60" aria-disabled>
          {loading ? 'searching…' : 'no matches'}
        </li>
      )}
      {options.map((opt, i) => (
        <li
          key={`${opt.create ? 'new-' : ''}${opt.id}`}
          id={optId(i)}
          role="option"
          aria-selected={i === safeActive}
          data-ta-index={i}
          onMouseDown={(e) => {
            // mousedown (not click) so the parent input does not blur-commit
            // before the selection lands.
            e.preventDefault()
            onSelect(opt)
          }}
          onMouseEnter={() => setActive(i)}
          className={cn(
            'flex cursor-pointer items-center justify-between gap-3 px-3 py-1.5',
            i === safeActive ? 'bg-accent/50' : 'hover:bg-accent/25',
            opt.create && 'border-t border-border',
          )}
        >
          <span className="min-w-0 flex-1 truncate">
            {opt.create ? (
              <span className="text-primary">+ {opt.label}</span>
            ) : (
              <span className="text-foreground">{opt.label}</span>
            )}
          </span>
          <span className="shrink-0 text-[10px] uppercase tracking-wider text-muted-foreground/60">
            {opt.sub}
          </span>
        </li>
      ))}
    </ul>
  )
}
