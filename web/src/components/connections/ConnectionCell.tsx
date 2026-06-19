// ConnectionCell — single (server, workspace) cell in the connections
// matrix. Pure presentation: takes a computed CellState, renders the
// right glyph + label, calls onClick on tap.
//
// We do NOT fetch OAuth status here. The drawer hits the per-scope
// status endpoint on open; the cell-state pipeline computes
// "needs-auth" from cheap signals (route exists, scope exists,
// has_secrets) only. Avoids N×M HTTP fetches just to render the grid.

import { Check, Circle, Plus, XOctagon, AlertTriangle } from 'lucide-react'

export type CellKind =
  | 'connected'    // route exists, scope (or scopeless allow) is usable
  | 'needs-auth'   // route exists but credentials missing or expired
  | 'add'          // no route — clicking creates one
  | 'disabled'     // route exists but policy=deny

export interface CellState {
  kind: CellKind
  // Optional short hint shown to the user (e.g. "20d left", "no creds").
  // Drives the secondary line under the glyph.
  hint?: string
  // The route id, if a route already exists. Used by the drawer to
  // resolve "edit existing" vs "create new".
  routeId?: string
}

export function ConnectionCell({
  state,
  onClick,
  serverLabel,
  workspaceLabel,
}: {
  state: CellState
  onClick: () => void
  serverLabel: string
  workspaceLabel: string
}) {
  const ariaLabel = `${serverLabel} in ${workspaceLabel}: ${describeKind(state.kind)}${state.hint ? ` (${state.hint})` : ''}`
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={ariaLabel}
      data-testid={`connection-cell-${state.kind}`}
      className={
        'group flex h-full min-h-[3.25rem] w-full flex-col items-center justify-center gap-0.5 ' +
        'rounded-md border px-2 py-1.5 text-xs transition-colors ' +
        kindClasses(state.kind)
      }
    >
      <span className="flex items-center gap-1 font-medium">
        {kindIcon(state.kind)}
        <span>{kindLabel(state.kind)}</span>
      </span>
      {state.hint && (
        <span className="text-[10px] text-muted-foreground/80">{state.hint}</span>
      )}
    </button>
  )
}

function kindIcon(kind: CellKind) {
  switch (kind) {
    case 'connected':
      return <Check className="h-3 w-3" />
    case 'needs-auth':
      return <AlertTriangle className="h-3 w-3" />
    case 'add':
      return <Plus className="h-3 w-3" />
    case 'disabled':
      return <XOctagon className="h-3 w-3" />
    default:
      return <Circle className="h-3 w-3" />
  }
}

function kindLabel(kind: CellKind): string {
  switch (kind) {
    case 'connected':
      return 'Connected'
    case 'needs-auth':
      return 'Needs auth'
    case 'add':
      return 'Add'
    case 'disabled':
      return 'Disabled'
  }
}

function describeKind(kind: CellKind): string {
  switch (kind) {
    case 'connected':
      return 'connected and ready'
    case 'needs-auth':
      return 'credentials needed'
    case 'add':
      return 'not connected - click to set up'
    case 'disabled':
      return 'blocked by deny rule'
  }
}

function kindClasses(kind: CellKind): string {
  switch (kind) {
    case 'connected':
      return 'border-emerald-500/30 bg-emerald-500/5 text-emerald-600 hover:bg-emerald-500/10'
    case 'needs-auth':
      return 'border-amber-500/40 bg-amber-500/10 text-amber-700 hover:bg-amber-500/20'
    case 'add':
      return 'border-dashed border-border/60 bg-transparent text-muted-foreground hover:border-primary/40 hover:bg-primary/5 hover:text-foreground'
    case 'disabled':
      return 'border-border/40 bg-muted/30 text-muted-foreground hover:bg-muted/50'
  }
}
