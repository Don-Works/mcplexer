import { Library, Layers, Plug } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { WorkspaceConnectionSummary } from './connection-model'

export function WorkspaceRail({
  summaries,
  selectedWorkspaceId,
  libraryActive,
  selectionSuppressed = false,
  onSelect,
  onOpenLibrary,
}: {
  summaries: WorkspaceConnectionSummary[]
  selectedWorkspaceId: string
  libraryActive: boolean
  // When another top-level surface owns the main pane (server library, or the
  // "New workspace" form) no workspace is the active context — suppress the
  // rail highlight so it never conflicts with what the pane is showing.
  selectionSuppressed?: boolean
  onSelect: (workspaceId: string) => void
  onOpenLibrary: () => void
}) {
  return (
    <aside className="min-w-0 border border-border/60 bg-card/20">
      <div className="hidden border-b border-border/60 px-3 py-2.5 lg:block">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <Layers className="h-4 w-4 text-muted-foreground" />
          Workspaces
        </div>
      </div>

      <div className="scrollbar-none flex gap-1.5 overflow-x-auto p-1.5 lg:block lg:max-h-[calc(100dvh-18rem)] lg:overflow-y-auto">
        {summaries.map((summary) => {
          const selected = !libraryActive && !selectionSuppressed && summary.workspace.id === selectedWorkspaceId
          return (
            <button
              key={summary.workspace.id}
              type="button"
              onClick={() => onSelect(summary.workspace.id)}
              aria-pressed={selected}
              className={
                'min-w-40 shrink-0 border px-3 py-2 text-left transition-colors lg:mb-1 lg:w-full lg:min-w-0 lg:last:mb-0 ' +
                (selected
                  ? 'border-primary/50 bg-primary/10 text-foreground'
                  : 'border-transparent text-muted-foreground hover:border-border/60 hover:bg-muted/30 hover:text-foreground')
              }
              data-testid={`connections-workspace-${summary.workspace.id}`}
            >
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="block truncate text-sm font-medium">{summary.workspace.name}</span>
                </TooltipTrigger>
                <TooltipContent side="right" className="max-w-xs font-mono text-xs">
                  {summary.workspace.root_path}
                </TooltipContent>
              </Tooltip>
              <div className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
                <span className="flex items-center gap-1">
                  <Plug className="h-3 w-3 text-emerald-500" />
                  <span className="tabular-nums">{summary.connected}</span>
                </span>
                {summary.needsAuth > 0 && (
                  <span className="text-amber-500">{summary.needsAuth} need auth</span>
                )}
                {summary.available > 0 && <span className="tabular-nums opacity-50">+{summary.available}</span>}
              </div>
            </button>
          )
        })}
        <div className="min-w-40 shrink-0 lg:mt-2 lg:min-w-0 lg:border-t lg:border-border/60 lg:pt-1.5">
          <button
            type="button"
            onClick={onOpenLibrary}
            aria-pressed={libraryActive}
            className={
              'flex w-full items-center gap-2 border px-3 py-3 text-left text-sm transition-colors lg:py-2 ' +
              (libraryActive
                ? 'border-primary/50 bg-primary/10 text-foreground'
                : 'border-transparent text-muted-foreground hover:border-border/60 hover:bg-muted/30 hover:text-foreground')
            }
          >
            <Library className="h-4 w-4" /> Server library
          </button>
        </div>
      </div>
    </aside>
  )
}
