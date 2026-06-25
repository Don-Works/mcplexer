import { useEffect, useRef, useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import type { AuditFilter } from '@/api/types'
import { cn } from '@/lib/utils'

export interface FacetOption {
  value: string
  label: string
}

// The actor kinds the gateway emits (store/models.go). Stable enough to hard
// code as the dropdown's options; free-text isn't needed here.
const ACTOR_KINDS: FacetOption[] = [
  { value: 'user', label: 'User' },
  { value: 'worker', label: 'Worker' },
  { value: 'scheduler', label: 'Scheduler' },
  { value: 'secrets', label: 'Secrets resolver' },
]

const STATUS_OPTIONS: FacetOption[] = [
  { value: 'success', label: 'Success' },
  { value: 'error', label: 'Error' },
  { value: 'blocked', label: 'Blocked' },
]

const TIME_PRESETS: { label: string; hours: number | null }[] = [
  { label: 'Last hour', hours: 1 },
  { label: 'Last 24h', hours: 24 },
  { label: 'Last 7d', hours: 24 * 7 },
  { label: 'All time', hours: null },
]

// Shared label for a facet group — uppercase, mono, muted.
function FacetLabel({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
      {children}
    </span>
  )
}

// FacetSelect — a single-select dropdown over an options list. Active state
// tints the trigger (primary), inactive is dashed/muted — matching the page's
// FilterBar chips. An "All" item clears.
function FacetSelect({
  label,
  value,
  options,
  allLabel,
  onSet,
  testId,
}: {
  label: string
  value: string | null
  options: FacetOption[]
  allLabel: string
  onSet: (value: string | null) => void
  testId?: string
}) {
  const active = value !== null && value !== ''
  const current = active ? options.find((o) => o.value === value)?.label ?? value : null
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          data-testid={testId}
          className={cn(
            'flex w-full items-center justify-between gap-1 border px-2 py-1.5 text-left font-mono text-xs transition-colors',
            active
              ? 'border-primary/40 bg-primary/5 text-foreground'
              : 'border-dashed border-border text-muted-foreground hover:border-border/80 hover:text-foreground',
          )}
        >
          <span className="truncate">
            {active ? <span className="text-primary">{current}</span> : label}
          </span>
          <ChevronDown className="h-3 w-3 shrink-0 opacity-60" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="max-h-72 min-w-[12rem] overflow-y-auto">
        <DropdownMenuItem onClick={() => onSet(null)}>
          <span className="text-muted-foreground">{allLabel}</span>
        </DropdownMenuItem>
        {options.map((opt) => (
          <DropdownMenuItem key={opt.value} onClick={() => onSet(opt.value)}>
            <Check className={cn('mr-2 h-3 w-3', value === opt.value ? 'opacity-100' : 'opacity-0')} />
            <span className="truncate">{opt.label}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// FacetText — a debounced-on-blur/Enter text input styled as a facet field.
function FacetText({
  label,
  value,
  placeholder,
  onSet,
  testId,
}: {
  label: string
  value: string | null
  placeholder?: string
  onSet: (value: string | null) => void
  testId?: string
}) {
  const [draft, setDraft] = useState(value ?? '')
  const inputRef = useRef<HTMLInputElement | null>(null)
  // Keep draft synced when the controlled value changes externally (clear-all,
  // applying a saved search) — but never clobber what the user is typing.
  useEffect(() => {
    if (document.activeElement !== inputRef.current) setDraft(value ?? '')
  }, [value])
  const active = value !== null && value !== ''
  return (
    <div className="space-y-1">
      <FacetLabel>{label}</FacetLabel>
      <Input
        ref={inputRef}
        value={draft}
        data-testid={testId}
        placeholder={placeholder}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') onSet(draft.trim() || null)
          else if (e.key === 'Escape') {
            setDraft(value ?? '')
            ;(e.target as HTMLInputElement).blur()
          }
        }}
        onBlur={() => onSet(draft.trim() || null)}
        className={cn(
          'h-8 rounded-none border-border bg-background font-mono text-xs focus-visible:ring-1 focus-visible:ring-primary/30',
          active && 'border-primary/40 bg-primary/5',
        )}
      />
    </div>
  )
}

// FacetToggle — tri-state pill group: All / On / Off (for cache_hit).
function CacheToggle({
  value,
  onSet,
}: {
  value: boolean | undefined
  onSet: (value: boolean | undefined) => void
}) {
  const opts: { label: string; v: boolean | undefined }[] = [
    { label: 'All', v: undefined },
    { label: 'Cached', v: true },
    { label: 'Live', v: false },
  ]
  return (
    <div className="space-y-1">
      <FacetLabel>Cache</FacetLabel>
      <div className="flex gap-1">
        {opts.map((o) => {
          const active = value === o.v
          return (
            <button
              key={o.label}
              type="button"
              onClick={() => onSet(o.v)}
              className={cn(
                'flex-1 border px-2 py-1 font-mono text-[11px] transition-colors',
                active
                  ? 'border-primary/40 bg-primary/5 text-foreground'
                  : 'border-dashed border-border text-muted-foreground hover:text-foreground',
              )}
            >
              {o.label}
            </button>
          )
        })}
      </div>
    </div>
  )
}

/**
 * AuditFacetRail — the vertical faceted filter rail for Mission Control.
 * Controlled via `filter` + `onChange(patch)`. Option lists (workspaces,
 * servers, routes) come from the page (it owns the fetches); actor kinds and
 * statuses are intrinsic. Compact mono aesthetic consistent with the page's
 * FilterBar chips (dashed inactive, primary-tinted active).
 */
export function AuditFacetRail({
  filter,
  onChange,
  workspaces,
  servers,
  routes,
  clientTypes,
  className,
}: {
  filter: AuditFilter
  onChange: (patch: Partial<AuditFilter>) => void
  workspaces?: FacetOption[]
  servers?: FacetOption[]
  routes?: FacetOption[]
  clientTypes?: FacetOption[]
  className?: string
}) {
  const applyPreset = (hours: number | null) => {
    if (hours === null) {
      onChange({ after: undefined, before: undefined })
      return
    }
    // The API parses `after` as RFC3339; a bare local datetime string fails
    // that parse and silently no-ops the filter. toISOString() is RFC3339 (UTC).
    const past = new Date(Date.now() - hours * 3600_000)
    onChange({ after: past.toISOString(), before: undefined })
  }

  const hasAny =
    filter.workspace_id ||
    filter.status ||
    filter.tool_name ||
    filter.actor_kind ||
    filter.downstream_server_id ||
    filter.route_rule_id ||
    filter.client_type ||
    filter.cache_hit !== undefined ||
    filter.min_latency_ms !== undefined ||
    filter.after ||
    filter.before

  return (
    <div className={cn('space-y-4', className)}>
      <div className="flex items-center justify-between">
        <FacetLabel>Filters</FacetLabel>
        {hasAny && (
          <button
            type="button"
            data-testid="audit-facet-clear"
            onClick={() =>
              onChange({
                workspace_id: undefined,
                status: undefined,
                tool_name: undefined,
                actor_kind: undefined,
                downstream_server_id: undefined,
                route_rule_id: undefined,
                client_type: undefined,
                cache_hit: undefined,
                min_latency_ms: undefined,
                after: undefined,
                before: undefined,
              })
            }
            className="text-[11px] text-muted-foreground hover:text-foreground"
          >
            Clear all
          </button>
        )}
      </div>

      {workspaces && (
        <div className="space-y-1">
          <FacetLabel>Workspace</FacetLabel>
          <FacetSelect
            label="Any workspace"
            value={filter.workspace_id ?? null}
            options={workspaces}
            allLabel="All workspaces"
            onSet={(v) => onChange({ workspace_id: v ?? undefined })}
            testId="audit-facet-workspace"
          />
        </div>
      )}

      <div className="space-y-1">
        <FacetLabel>Status</FacetLabel>
        <FacetSelect
          label="Any status"
          value={filter.status ?? null}
          options={STATUS_OPTIONS}
          allLabel="All statuses"
          onSet={(v) => onChange({ status: (v as AuditFilter['status']) ?? undefined })}
          testId="audit-facet-status"
        />
      </div>

      <FacetText
        label="Tool"
        value={filter.tool_name ?? null}
        placeholder="tool name…"
        onSet={(v) => onChange({ tool_name: v ?? undefined })}
        testId="audit-facet-tool"
      />

      <div className="space-y-1">
        <FacetLabel>Actor</FacetLabel>
        <FacetSelect
          label="Any actor"
          value={filter.actor_kind ?? null}
          options={ACTOR_KINDS}
          allLabel="All actors"
          onSet={(v) => onChange({ actor_kind: v ?? undefined })}
          testId="audit-facet-actor"
        />
      </div>

      {servers && (
        <div className="space-y-1">
          <FacetLabel>Downstream</FacetLabel>
          <FacetSelect
            label="Any server"
            value={filter.downstream_server_id ?? null}
            options={servers}
            allLabel="All servers"
            onSet={(v) => onChange({ downstream_server_id: v ?? undefined })}
            testId="audit-facet-server"
          />
        </div>
      )}

      {routes && (
        <div className="space-y-1">
          <FacetLabel>Route</FacetLabel>
          <FacetSelect
            label="Any route"
            value={filter.route_rule_id ?? null}
            options={routes}
            allLabel="All routes"
            onSet={(v) => onChange({ route_rule_id: v ?? undefined })}
            testId="audit-facet-route"
          />
        </div>
      )}

      {clientTypes && clientTypes.length > 0 ? (
        <div className="space-y-1">
          <FacetLabel>Harness</FacetLabel>
          <FacetSelect
            label="Any harness"
            value={filter.client_type ?? null}
            options={clientTypes}
            allLabel="All harnesses"
            onSet={(v) => onChange({ client_type: v ?? undefined })}
            testId="audit-facet-client"
          />
        </div>
      ) : (
        <FacetText
          label="Harness"
          value={filter.client_type ?? null}
          placeholder="client type…"
          onSet={(v) => onChange({ client_type: v ?? undefined })}
          testId="audit-facet-client"
        />
      )}

      <CacheToggle
        value={filter.cache_hit}
        onSet={(v) => onChange({ cache_hit: v })}
      />

      <FacetText
        label="Min latency (ms)"
        value={filter.min_latency_ms !== undefined ? String(filter.min_latency_ms) : null}
        placeholder="e.g. 1000"
        onSet={(v) => {
          const n = v === null ? NaN : Number(v)
          onChange({ min_latency_ms: Number.isFinite(n) && n > 0 ? n : undefined })
        }}
        testId="audit-facet-latency"
      />

      <div className="space-y-1">
        <FacetLabel>Time</FacetLabel>
        <div className="grid grid-cols-2 gap-1">
          {TIME_PRESETS.map((p) => {
            const active =
              p.hours === null
                ? !filter.after && !filter.before
                : false
            return (
              <button
                key={p.label}
                type="button"
                onClick={() => applyPreset(p.hours)}
                className={cn(
                  'border px-2 py-1 font-mono text-[11px] transition-colors',
                  active
                    ? 'border-primary/40 bg-primary/5 text-foreground'
                    : 'border-dashed border-border text-muted-foreground hover:text-foreground',
                )}
              >
                {p.label}
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
