import {
  AlertTriangle,
  CheckCircle2,
  KeyRound,
  Plus,
  ShieldOff,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { scopeLabel } from '@/lib/scope-label'
import type { CellKind } from './ConnectionCell'
import type { WorkspaceConnectionRow } from './connection-model'

export function ConnectionSection({
  title,
  rows,
  onOpen,
}: {
  title: string
  rows: WorkspaceConnectionRow[]
  onOpen: (row: WorkspaceConnectionRow) => void
}) {
  return (
    <section className="rounded-md border border-border/50 bg-card/20">
      <div className="flex items-center justify-between border-b border-border/50 px-3 py-2">
        <h3 className="text-sm font-semibold">{title}</h3>
        <span className="text-xs text-muted-foreground">{rows.length}</span>
      </div>
      <div className="divide-y divide-border/50">
        {rows.map((row) => (
          <ConnectionRowButton key={row.server.id} row={row} onOpen={() => onOpen(row)} />
        ))}
      </div>
    </section>
  )
}

function ConnectionRowButton({ row, onOpen }: { row: WorkspaceConnectionRow; onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="grid w-full min-w-0 items-center gap-2 px-3 py-3 text-left transition-colors hover:bg-muted/30 focus:outline-none focus:ring-1 focus:ring-primary/60 md:grid-cols-[1fr_auto]"
      aria-label={`${row.server.name}: ${statusLabel(row.state.kind)}`}
      data-testid={`connection-cell-${row.server.id}-${row.workspace.id}`}
      data-state-kind={row.state.kind}
    >
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <StatusIcon kind={row.state.kind} />
          <span className="truncate text-sm font-medium text-foreground">{row.server.name}</span>
          <Badge
            variant="outline"
            tone={kindTone(row.state.kind)}
            className="text-[10px] px-1.5"
          >
            {statusLabel(row.state.kind)}
          </Badge>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
          <span className="font-mono text-muted-foreground/70">{row.server.tool_namespace}__*</span>
          <span className="text-muted-foreground/40 select-none">·</span>
          <span>{row.server.transport}</span>
          {row.state.kind !== 'add' && (
            <>
              <span className="text-muted-foreground/40 select-none">·</span>
              <span className="inline-flex items-center gap-1">
                <KeyRound className="h-3 w-3 shrink-0" />
                <span className="truncate">{credentialLabel(row)}</span>
              </span>
            </>
          )}
        </div>
      </div>
      <div className="flex items-center justify-end gap-2">
        {row.route && <span className="hidden truncate font-mono text-xs text-muted-foreground/60 lg:inline">{row.route.path_glob}</span>}
        <span className="inline-flex shrink-0 items-center gap-1 rounded-sm border border-border/60 px-2 py-1 text-xs text-foreground hover:border-primary/40 hover:bg-primary/5">
          {actionLabel(row.state.kind)}
        </span>
      </div>
    </button>
  )
}

function StatusIcon({ kind }: { kind: CellKind }) {
  const className = 'h-4 w-4 shrink-0'
  switch (kind) {
    case 'connected':
      return <CheckCircle2 className={`${className} text-emerald-500`} />
    case 'needs-auth':
      return <AlertTriangle className={`${className} text-amber-500`} />
    case 'disabled':
      return <ShieldOff className={`${className} text-muted-foreground`} />
    case 'add':
      return <Plus className={`${className} text-muted-foreground`} />
  }
}

function credentialLabel(row: WorkspaceConnectionRow): string {
  if (!row.route) return 'Choose during connect'
  if (!row.route.auth_scope_id) return 'No credential required'
  if (row.scope) return scopeLabel(row.scope)
  return 'Missing credential'
}

function statusLabel(kind: CellKind): string {
  switch (kind) {
    case 'connected':
      return 'Connected'
    case 'needs-auth':
      return 'Needs auth'
    case 'disabled':
      return 'Disabled'
    case 'add':
      return 'Available'
  }
}

function actionLabel(kind: CellKind): string {
  switch (kind) {
    case 'connected':
      return 'Edit'
    case 'needs-auth':
      return 'Fix auth'
    case 'disabled':
      return 'Review'
    case 'add':
      return 'Connect'
  }
}

function kindTone(kind: CellKind): 'success' | 'warn' | 'muted' {
  if (kind === 'connected') return 'success'
  if (kind === 'needs-auth') return 'warn'
  return 'muted'
}
