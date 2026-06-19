// ConnectionMatrix — sticky-edges 2D grid: rows = servers, cols =
// workspaces. The cell-state derivation lives in the parent so the
// matrix stays a pure renderer (easier to test, easier to memo).
//
// Layout choice: a CSS grid (instead of a <table>) so we get clean
// sticky behaviour on both the first column (server names) AND the
// first row (workspace names) without the rendering edge-cases <thead>
// + position: sticky historically run into.

import { useMemo } from 'react'
import type { DownstreamServer, Workspace } from '@/api/types'
import { ConnectionCell, type CellState } from './ConnectionCell'

export interface ConnectionMatrixProps {
  servers: DownstreamServer[]
  workspaces: Workspace[]
  // Lookup keyed by `${server_id}::${workspace_id}`. The parent
  // computes this; we just render. Missing keys default to 'add'.
  cells: Map<string, CellState>
  onCellClick: (
    server: DownstreamServer,
    workspace: Workspace,
    state: CellState,
  ) => void
}

export function ConnectionMatrix({
  servers,
  workspaces,
  cells,
  onCellClick,
}: ConnectionMatrixProps) {
  // Column template: one for the row-header column + one per workspace.
  // minmax keeps long workspace names tidy.
  const gridTemplateColumns = useMemo(
    () =>
      `minmax(11rem, 14rem) repeat(${Math.max(workspaces.length, 1)}, minmax(9rem, 1fr))`,
    [workspaces.length],
  )

  if (servers.length === 0) {
    return (
      <EmptyState
        title="No servers installed yet"
        body="Connect a service from the sidebar to populate this matrix."
      />
    )
  }
  if (workspaces.length === 0) {
    return (
      <EmptyState
        title="No workspaces yet"
        body="Create a workspace to wire your installed servers to it."
      />
    )
  }

  return (
    <div className="overflow-auto rounded-md border border-border/40">
      <div className="min-w-fit">
        {/* Header row: empty corner + workspace labels */}
        <div
          className="sticky top-0 z-20 grid border-b border-border/40 bg-background/95 backdrop-blur"
          style={{ gridTemplateColumns }}
        >
          <div className="sticky left-0 z-30 border-r border-border/40 bg-background/95 px-3 py-2 text-[10px] uppercase tracking-wider text-muted-foreground">
            Server \ Workspace
          </div>
          {workspaces.map((w) => (
            <div
              key={w.id}
              data-testid={`connection-col-${w.id}`}
              className="border-l border-border/30 px-3 py-2 text-sm font-medium"
            >
              <div className="truncate">{w.name}</div>
              <div className="truncate text-[10px] text-muted-foreground">
                {w.root_path}
              </div>
            </div>
          ))}
        </div>

        {/* Body rows */}
        {servers.map((s) => (
          <div
            key={s.id}
            className="grid border-b border-border/20 last:border-b-0"
            style={{ gridTemplateColumns }}
          >
            <div
              data-testid={`connection-row-${s.id}`}
              className="sticky left-0 z-10 flex flex-col justify-center gap-0.5 border-r border-border/40 bg-background/95 px-3 py-2 backdrop-blur"
            >
              <div className="truncate text-sm font-medium">{s.name}</div>
              <div className="truncate text-[10px] text-muted-foreground">
                {s.tool_namespace} · {s.transport}
              </div>
            </div>
            {workspaces.map((w) => {
              const key = cellKey(s.id, w.id)
              const state = cells.get(key) ?? { kind: 'add' as const }
              return (
                <div
                  key={w.id}
                  data-testid={`connection-cell-${s.id}-${w.id}`}
                  className="border-l border-border/20 p-1.5"
                >
                  <ConnectionCell
                    state={state}
                    onClick={() => onCellClick(s, w, state)}
                    serverLabel={s.name}
                    workspaceLabel={w.name}
                  />
                </div>
              )
            })}
          </div>
        ))}
      </div>
    </div>
  )
}

export function cellKey(serverId: string, workspaceId: string): string {
  return `${serverId}::${workspaceId}`
}

function EmptyState({ title, body }: { title: string; body: string }) {
  return (
    <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-6 py-12 text-center">
      <p className="text-sm font-medium">{title}</p>
      <p className="mt-1 text-xs text-muted-foreground">{body}</p>
    </div>
  )
}
