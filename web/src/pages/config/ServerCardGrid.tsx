import type { CatalogEntry } from '@/data/server-catalog'
import { CATEGORY_LABELS, CATEGORY_ORDER } from '@/data/server-catalog'
import type { AuthScope, DownstreamOAuthStatusEntry, DownstreamServer } from '@/api/types'
import { ServerCard } from './ServerCard'
import { Server } from 'lucide-react'

export interface MergedServer {
  catalog: CatalogEntry | null
  db: DownstreamServer | null
}

interface ServerCardGridProps {
  servers: MergedServer[]
  oauthStatuses: Record<string, DownstreamOAuthStatusEntry[]>
  statusErrors: Record<string, boolean>
  serverAuthScopes: Record<string, AuthScope>
  onAdd: (catalog: CatalogEntry) => void
  onEnable: (ds: DownstreamServer) => void
  onDisable: (ds: DownstreamServer) => void
  onEdit: (ds: DownstreamServer) => void
  onDuplicate: (ds: DownstreamServer) => void
  onDelete: (ds: DownstreamServer) => void
  onConnect: (ds: DownstreamServer) => void
  onApiKey: (ds: DownstreamServer, scope: AuthScope) => void
  onOpenHammerspoon?: (ds: DownstreamServer) => void
}

export function ServerCardGrid({
  servers,
  oauthStatuses,
  statusErrors,
  serverAuthScopes,
  onAdd,
  onEnable,
  onDisable,
  onEdit,
  onDuplicate,
  onDelete,
  onConnect,
  onApiKey,
  onOpenHammerspoon,
}: ServerCardGridProps) {
  if (servers.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
        <Server className="mb-2 h-8 w-8 text-muted-foreground/50" />
        <p className="text-sm">No servers match your search</p>
      </div>
    )
  }

  // Group by category
  const grouped = new Map<string, MergedServer[]>()
  for (const s of servers) {
    const cat = s.catalog?.category ?? 'custom'
    const list = grouped.get(cat) ?? []
    list.push(s)
    grouped.set(cat, list)
  }

  // Render in category order, then custom at end
  const orderedKeys = [
    ...CATEGORY_ORDER.filter((c) => grouped.has(c)),
    ...(grouped.has('custom') ? ['custom'] : []),
  ]

  return (
    <div className="space-y-6">
      {orderedKeys.map((cat) => (
        <div key={cat}>
          <h2 className="mb-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            {cat === 'custom' ? 'Custom Servers' : CATEGORY_LABELS[cat as keyof typeof CATEGORY_LABELS]}
          </h2>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {grouped.get(cat)!.map((s) => (
              <ServerCard
                key={s.catalog?.id ?? s.db?.id}
                catalog={s.catalog}
                db={s.db}
                oauthStatuses={oauthStatuses}
                statusErrors={statusErrors}
                serverAuthScopes={serverAuthScopes}
                onAdd={onAdd}
                onEnable={onEnable}
                onDisable={onDisable}
                onEdit={onEdit}
                onDuplicate={onDuplicate}
                onDelete={onDelete}
                onConnect={onConnect}
                onApiKey={onApiKey}
                onOpenHammerspoon={onOpenHammerspoon}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}
