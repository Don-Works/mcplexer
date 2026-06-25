import { Link } from 'react-router-dom'
import { Layers, Plug, Plus, Search } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { Workspace } from '@/api/types'
import {
  type ConnectionFilter,
  type WorkspaceConnectionRow,
  type WorkspaceConnectionSummary,
} from './connection-model'
import { WorkspaceRail } from './WorkspaceRail'
import { ConnectionSection } from './ConnectionRows'
import { ConnectionFilterChips } from './ConnectionFilterChips'
import {
  ConnectionsSkeleton,
  EmptySetupState,
  ErrorBlock,
  NoMatches,
} from './ConnectionEmptyStates'
import {
  WorkspaceCommandCenter,
  type WorkspaceCommandCenterData,
} from './WorkspaceCommandCenter'

interface Props {
  workspaces: Workspace[]
  serverCount: number
  summaries: WorkspaceConnectionSummary[]
  selectedWorkspace: Workspace | null
  rows: WorkspaceConnectionRow[]
  visibleRows: WorkspaceConnectionRow[]
  counts: { all: number; connected: number; needsAuth: number; available: number }
  operations: WorkspaceCommandCenterData
  filter: ConnectionFilter
  query: string
  loading: boolean
  error: string | null
  onRetry: () => void
  onSelectWorkspace: (workspaceId: string) => void
  onFilterChange: (filter: ConnectionFilter) => void
  onQueryChange: (query: string) => void
  onOpenConnection: (row: WorkspaceConnectionRow) => void
}

export function WorkspaceConnectionsView({
  workspaces,
  serverCount,
  summaries,
  selectedWorkspace,
  rows,
  visibleRows,
  counts,
  operations,
  filter,
  query,
  loading,
  error,
  onRetry,
  onSelectWorkspace,
  onFilterChange,
  onQueryChange,
  onOpenConnection,
}: Props) {
  if (loading && (serverCount === 0 || workspaces.length === 0)) return <ConnectionsSkeleton />
  if (error && (serverCount === 0 || workspaces.length === 0)) {
    return (
      <div className="space-y-4">
        <ErrorBlock message={error} onRetry={onRetry} />
        <ConnectionsSkeleton />
      </div>
    )
  }
  if (serverCount === 0) return <NoServers />
  if (workspaces.length === 0) return <NoWorkspaces />
  if (!selectedWorkspace) return null

  const attentionRows = visibleRows.filter((row) => row.state.kind === 'needs-auth')
  const attachedRows = visibleRows.filter((row) => row.route && row.state.kind !== 'needs-auth')
  const availableRows = visibleRows.filter((row) => !row.route)
  const selectedSummary = summaries.find((s) => s.workspace.id === selectedWorkspace.id)

  return (
    <div className="space-y-4">
      {error && <ErrorBlock message={error} onRetry={onRetry} />}

      <div className="grid gap-4 xl:grid-cols-[18rem_1fr]">
        <div className="order-2 xl:order-1">
          <WorkspaceRail
            summaries={summaries}
            selectedWorkspaceId={selectedWorkspace.id}
            onSelect={onSelectWorkspace}
          />
        </div>

        <section className="order-1 min-w-0 space-y-4 xl:order-2">
          <WorkspaceCommandCenter
            workspace={selectedWorkspace}
            summary={selectedSummary}
            rows={rows}
            operations={operations}
            onOpenConnection={onOpenConnection}
          />
          <ConnectionToolbar
            query={query}
            filter={filter}
            counts={counts}
            onQueryChange={onQueryChange}
            onFilterChange={onFilterChange}
          />
          <ConnectionSections
            rows={rows}
            visibleRows={visibleRows}
            attentionRows={attentionRows}
            attachedRows={attachedRows}
            availableRows={availableRows}
            query={query}
            filter={filter}
            onOpenConnection={onOpenConnection}
          />
        </section>
      </div>
    </div>
  )
}

function ConnectionToolbar({
  query,
  filter,
  counts,
  onQueryChange,
  onFilterChange,
}: {
  query: string
  filter: ConnectionFilter
  counts: Props['counts']
  onQueryChange: (query: string) => void
  onFilterChange: (filter: ConnectionFilter) => void
}) {
  return (
    <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
      <div className="relative min-w-64 flex-1 md:max-w-md">
        <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground/70" />
        <Input
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          placeholder="Search servers, credentials, namespaces"
          className="pl-8"
          data-testid="connections-search"
        />
      </div>
      <ConnectionFilterChips counts={counts} filter={filter} onChange={onFilterChange} />
    </div>
  )
}

function ConnectionSections({
  rows,
  visibleRows,
  attentionRows,
  attachedRows,
  availableRows,
  query,
  filter,
  onOpenConnection,
}: {
  rows: WorkspaceConnectionRow[]
  visibleRows: WorkspaceConnectionRow[]
  attentionRows: WorkspaceConnectionRow[]
  attachedRows: WorkspaceConnectionRow[]
  availableRows: WorkspaceConnectionRow[]
  query: string
  filter: ConnectionFilter
  onOpenConnection: (row: WorkspaceConnectionRow) => void
}) {
  if (visibleRows.length === 0) return <NoMatches query={query} filter={filter} />

  return (
    <div className="space-y-4">
      {attentionRows.length > 0 && (
        <ConnectionSection
          title="Needs Attention"
          rows={attentionRows}
          onOpen={onOpenConnection}
        />
      )}
      {attachedRows.length > 0 && (
        <ConnectionSection
          title="Configured In This Workspace"
          rows={attachedRows}
          onOpen={onOpenConnection}
        />
      )}
      {availableRows.length > 0 && (
        <ConnectionSection
          key={`${availableRows[0]?.workspace.id ?? 'workspace'}-${filter}-${query.trim() ? 'searched' : 'default'}`}
          title={rows.some((row) => row.route) ? 'Available To Connect' : 'Available Servers'}
          rows={availableRows}
          onOpen={onOpenConnection}
          collapsible
          defaultCollapsed={filter === 'all' && query.trim() === ''}
        />
      )}
    </div>
  )
}

function NoServers() {
  return (
    <EmptySetupState
      icon={<Plug className="h-7 w-7" />}
      title="No servers configured"
      body="Add a server first, then connect it to workspaces where agents should use it."
      action={
        <Button asChild>
          <Link to="/setup">
            <Plus className="mr-1.5 h-4 w-4" />
            Add server
          </Link>
        </Button>
      }
    />
  )
}

function NoWorkspaces() {
  return (
    <EmptySetupState
      icon={<Layers className="h-7 w-7" />}
      title="No workspaces yet"
      body="Create a workspace so each project has a clear service boundary."
      action={
        <Button asChild>
          <Link to="/workspaces/manage">
            <Plus className="mr-1.5 h-4 w-4" />
            New workspace
          </Link>
        </Button>
      }
    />
  )
}
