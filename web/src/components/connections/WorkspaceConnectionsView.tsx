import { Search } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { ConnectionSection } from './ConnectionRows'
import { ConnectionFilterChips } from './ConnectionFilterChips'
import { NoMatches } from './ConnectionEmptyStates'
import type { ConnectionFilter, WorkspaceConnectionRow } from './connection-model'

export function WorkspaceConnectionsView({
  rows,
  visibleRows,
  counts,
  filter,
  query,
  onFilterChange,
  onQueryChange,
  onOpenConnection,
}: {
  rows: WorkspaceConnectionRow[]
  visibleRows: WorkspaceConnectionRow[]
  counts: { all: number; connected: number; needsAuth: number; available: number; denied: number }
  filter: ConnectionFilter
  query: string
  onFilterChange: (filter: ConnectionFilter) => void
  onQueryChange: (query: string) => void
  onOpenConnection: (row: WorkspaceConnectionRow) => void
}) {
  const attentionRows = visibleRows.filter((row) => row.state.kind === 'needs-auth')
  const deniedRows = visibleRows.filter((row) => row.state.kind === 'disabled')
  const configuredRows = visibleRows.filter(
    (row) => row.routes.length > 0 && row.state.kind !== 'needs-auth' && row.state.kind !== 'disabled',
  )
  const availableRows = visibleRows.filter((row) => row.routes.length === 0)

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative min-w-64 flex-1 sm:max-w-md">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground/70" />
          <Input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder="Search servers, credentials, namespaces"
            className="pl-8"
            data-testid="connections-search"
          />
        </div>
      </div>

      <ConnectionFilterChips counts={counts} filter={filter} onChange={onFilterChange} />

      {visibleRows.length === 0 ? (
        <NoMatches query={query} filter={filter} />
      ) : (
        <div className="space-y-4">
          {attentionRows.length > 0 && (
            <ConnectionSection title="Needs attention" rows={attentionRows} onOpen={onOpenConnection} />
          )}
          {configuredRows.length > 0 && (
            <ConnectionSection title="Has access" rows={configuredRows} onOpen={onOpenConnection} />
          )}
          {deniedRows.length > 0 && (
            <ConnectionSection title="Denied" rows={deniedRows} onOpen={onOpenConnection} />
          )}
          {availableRows.length > 0 && (
            <ConnectionSection
              key={`${availableRows[0]?.workspace.id ?? 'workspace'}-${filter}-${query.trim() ? 'searched' : 'default'}`}
              title={rows.some((row) => row.routes.length > 0) ? 'Available to add' : 'Available servers'}
              rows={availableRows}
              onOpen={onOpenConnection}
              collapsible
              defaultCollapsed={filter === 'all' && query.trim() === '' && configuredRows.length > 4}
            />
          )}
        </div>
      )}
    </div>
  )
}
