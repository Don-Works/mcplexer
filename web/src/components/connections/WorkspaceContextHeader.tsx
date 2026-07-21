import { Activity, KeyRound, Plus, Settings2 } from 'lucide-react'
import type { Workspace } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { WorkspaceConnectionSummary } from './connection-model'

export type WorkspaceSection = 'access' | 'activity' | 'settings'

const SECTIONS: Array<{
  id: WorkspaceSection
  label: string
  icon: typeof KeyRound
}> = [
  { id: 'access', label: 'Access', icon: KeyRound },
  { id: 'activity', label: 'Activity', icon: Activity },
  { id: 'settings', label: 'Settings', icon: Settings2 },
]

export function WorkspaceContextHeader({
  workspace,
  summary,
  section,
  onSectionChange,
  onAddServer,
}: {
  workspace: Workspace
  summary?: WorkspaceConnectionSummary
  section: WorkspaceSection
  onSectionChange: (section: WorkspaceSection) => void
  onAddServer: () => void
}) {
  return (
    <div className="border border-border/60 bg-card/20">
      <div className="flex items-start justify-between gap-3 px-3 py-2.5 sm:px-4 sm:py-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="truncate text-lg font-semibold">{workspace.name}</h2>
            <Badge variant="outline" tone={workspace.default_policy === 'allow' ? 'success' : 'muted'}>
              default {workspace.default_policy}
            </Badge>
          </div>
          <p className="mt-1 truncate font-mono text-xs text-muted-foreground" title={workspace.root_path}>
            {workspace.root_path || 'No root path set'}
          </p>
          <div className="mt-1.5 flex flex-wrap gap-1.5 text-xs text-muted-foreground">
            <span>{summary?.connected ?? 0} usable</span>
            {Boolean(summary?.needsAuth) && <span className="text-amber-500">{summary?.needsAuth} need auth</span>}
            {Boolean(summary?.disabled) && <span>{summary?.disabled} denied</span>}
            <span>{summary?.available ?? 0} available</span>
          </div>
        </div>
        <Button size="sm" className="shrink-0" onClick={onAddServer}>
          <Plus className="mr-1.5 h-4 w-4" />
          <span className="sm:hidden">Add</span>
          <span className="hidden sm:inline">Add server</span>
        </Button>
      </div>

      <nav className="scrollbar-none flex overflow-x-auto border-t border-border/60 px-2" aria-label="Workspace configuration">
        {SECTIONS.map(({ id, label, icon: Icon }) => {
          const active = section === id
          return (
            <button
              key={id}
              type="button"
              onClick={() => onSectionChange(id)}
              aria-current={active ? 'page' : undefined}
              className={
                'relative flex items-center gap-1.5 px-3 py-2 text-sm transition-colors sm:py-2.5 ' +
                (active ? 'text-foreground' : 'text-muted-foreground hover:text-foreground')
              }
              data-testid={`workspace-section-${id}`}
            >
              <Icon className="h-3.5 w-3.5" /> {label}
              {active && <span className="absolute inset-x-2 bottom-0 h-0.5 bg-primary" />}
            </button>
          )
        })}
      </nav>
    </div>
  )
}
