import { Link } from 'react-router-dom'
import { Layers, Network, Plug, Settings2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Workspace } from '@/api/types'
import type { WorkspaceConnectionSummary } from './connection-model'

export function WorkspaceRail({
  summaries,
  selectedWorkspaceId,
  onSelect,
}: {
  summaries: WorkspaceConnectionSummary[]
  selectedWorkspaceId: string
  onSelect: (workspaceId: string) => void
}) {
  return (
    <aside className="min-w-0 rounded-md border border-border/50 bg-card/30">
      <div className="border-b border-border/50 px-3 py-2">
        <div className="flex items-center gap-2 text-sm font-medium">
          <Layers className="h-4 w-4 text-muted-foreground" />
          Workspaces
        </div>
      </div>
      <div className="max-h-[70vh] overflow-auto p-1.5">
        {summaries.map((summary) => {
          const selected = summary.workspace.id === selectedWorkspaceId
          return (
            <button
              key={summary.workspace.id}
              type="button"
              onClick={() => onSelect(summary.workspace.id)}
              aria-pressed={selected}
              className={
                'mb-1 w-full min-w-0 rounded-md border px-3 py-2 text-left transition-colors last:mb-0 ' +
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
                  <span className="flex items-center gap-1 text-amber-500">
                    <span className="tabular-nums">{summary.needsAuth}</span>
                    <span>need creds</span>
                  </span>
                )}
                {summary.available > 0 && (
                  <span className="tabular-nums opacity-50">+{summary.available}</span>
                )}
              </div>
            </button>
          )
        })}
      </div>
    </aside>
  )
}

export function WorkspaceHeader({
  workspace,
  summary,
}: {
  workspace: Workspace
  summary?: WorkspaceConnectionSummary
}) {
  return (
    <div className="rounded-md border border-border/50 bg-card/30 px-4 py-3">
      <div className="flex flex-col gap-2 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex items-center gap-2 min-w-0">
          <Network className="h-4 w-4 shrink-0 text-muted-foreground" />
          <h2 className="truncate text-lg font-semibold">{workspace.name}</h2>
          <span className="hidden truncate font-mono text-xs text-muted-foreground/50 sm:inline" title={workspace.root_path}>
            {workspace.root_path}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline" tone={workspace.default_policy === 'allow' ? 'success' : 'muted'}>
            default {workspace.default_policy}
          </Badge>
          <Badge variant="outline" tone="success">{summary?.connected ?? 0} usable</Badge>
          {summary && summary.needsAuth > 0 && (
            <Badge variant="outline" tone="warn">{summary.needsAuth} need creds</Badge>
          )}
          {summary && summary.available > 0 && (
            <Badge variant="outline" tone="muted">{summary.available} available</Badge>
          )}
          <Button variant="outline" size="sm" asChild className="h-7">
            <Link to={`/workspaces/manage?workspace=${workspace.id}`}>
              <Settings2 className="mr-1.5 h-3.5 w-3.5" />
              Edit
            </Link>
          </Button>
        </div>
      </div>
    </div>
  )
}
