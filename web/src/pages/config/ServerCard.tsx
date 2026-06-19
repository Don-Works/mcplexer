import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { CatalogEntry } from '@/data/server-catalog'
import { CATEGORY_LABELS } from '@/data/server-catalog'
import type { AuthScope, DownstreamOAuthStatusEntry, DownstreamServer } from '@/api/types'
import { Copy, Key, Link, Pencil, Plus, Trash2 } from 'lucide-react'
import { getOAuthBadges } from './DownstreamOAuthBadges'
import { HammerspoonStatusButton } from './HammerspoonPanel'

type ServerStatus = 'not-added' | 'disabled' | 'active' | 'needs-auth'

interface ServerCardProps {
  catalog: CatalogEntry | null
  db: DownstreamServer | null
  oauthStatuses: Record<string, DownstreamOAuthStatusEntry[]>
  statusErrors: Record<string, boolean>
  serverAuthScopes: Record<string, AuthScope>
  onAdd: (catalog: CatalogEntry) => void
  onEnable: (ds: DownstreamServer) => void
  onEdit: (ds: DownstreamServer) => void
  onDuplicate: (ds: DownstreamServer) => void
  onDelete: (ds: DownstreamServer) => void
  onConnect: (ds: DownstreamServer) => void
  onApiKey: (ds: DownstreamServer, scope: AuthScope) => void
  /** Open the Hammerspoon bridge detail panel — keyed off catalog.id ===
   *  'hammerspoon'. Only the page that has the panel mounted passes this. */
  onOpenHammerspoon?: (ds: DownstreamServer) => void
}

function getStatus(
  db: DownstreamServer | null,
  serverAuthScopes: Record<string, AuthScope>,
): ServerStatus {
  if (!db) return 'not-added'
  if (db.disabled) return 'disabled'
  const scope = serverAuthScopes[db.id]
  if (scope && !scope.has_secrets) return 'needs-auth'
  return 'active'
}

const AUTH_LABELS: Record<string, string> = {
  none: 'No auth',
  'api-key': 'API key',
  oauth: 'OAuth',
  config: 'Config',
}

export function ServerCard({
  catalog,
  db,
  oauthStatuses,
  statusErrors,
  serverAuthScopes,
  onAdd,
  onEnable,
  onEdit,
  onDuplicate,
  onDelete,
  onConnect,
  onApiKey,
  onOpenHammerspoon,
}: ServerCardProps) {
  const name = catalog?.name ?? db?.name ?? 'Unknown'
  const description = catalog?.description ?? ''
  const category = catalog?.category
  const auth = catalog?.auth
  const status = getStatus(db, serverAuthScopes)
  const isHammerspoon = (catalog?.id ?? db?.id) === 'hammerspoon'

  return (
    <Card className={`relative flex flex-col transition-colors ${status === 'disabled' ? 'opacity-60' : ''}`}>
      <CardContent className="flex flex-1 flex-col gap-3 p-4">
        {/* Header row */}
        <div className="flex items-start justify-between gap-2">
          <div className="flex flex-wrap gap-1.5">
            {category && (
              <Badge variant="secondary" className="text-[10px] font-medium">
                {CATEGORY_LABELS[category]}
              </Badge>
            )}
          </div>
          {auth && (
            <Badge variant="outline" className="shrink-0 text-[10px]">
              {AUTH_LABELS[auth] ?? auth}
            </Badge>
          )}
        </div>

        {/* Name + description */}
        <div className="flex-1">
          <h3 className="text-sm font-semibold leading-tight">{name}</h3>
          {description && (
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground line-clamp-2">
              {description}
            </p>
          )}
        </div>

        {/* OAuth badges for HTTP servers */}
        {db && db.transport === 'http' && (
          <div className="flex flex-wrap gap-1 min-w-0 overflow-hidden">
            {getOAuthBadges(db, oauthStatuses, statusErrors, onConnect)}
          </div>
        )}

        {/* Hammerspoon-specific status pill — opens the install/probe panel. */}
        {db && isHammerspoon && onOpenHammerspoon && (
          <div className="flex flex-wrap gap-1 min-w-0 overflow-hidden">
            <HammerspoonStatusButton server={db} onOpen={() => onOpenHammerspoon(db)} />
          </div>
        )}

        {/* Actions row */}
        <div className="flex items-center justify-between gap-1 pt-1">
          {/* Primary action */}
          <div>
            {status === 'not-added' && catalog && (
              <Button
                variant="outline"
                size="sm"
                className="h-7 gap-1 text-xs"
                data-testid={`server-add-${catalog.id}`}
                onClick={() => onAdd(catalog)}
              >
                <Plus className="h-3 w-3" />
                Add
              </Button>
            )}
            {status === 'disabled' && db && (
              <Button
                variant="outline"
                size="sm"
                className="h-7 gap-1 border-emerald-300 text-xs text-emerald-600 hover:bg-emerald-500/10"
                data-testid={`server-enable-${db.id}`}
                onClick={() => onEnable(db)}
              >
                Enable
              </Button>
            )}
            {status === 'active' && (
              <Badge className="border-0 bg-emerald-500/15 text-emerald-600 text-xs">
                Active
              </Badge>
            )}
            {status === 'needs-auth' && db && (
              <Button
                variant="outline"
                size="sm"
                className="h-7 gap-1 border-amber-300 text-xs text-amber-600 hover:bg-amber-500/10"
                onClick={() => {
                  const scope = serverAuthScopes[db.id]
                  if (scope) onApiKey(db, scope)
                }}
              >
                <Key className="h-3 w-3" />
                Setup Required
              </Button>
            )}
          </div>

          {/* Management icons — only for servers in DB */}
          {db && (
            <div className="flex gap-0.5">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 w-7 p-0"
                    aria-label={`Edit ${name}`}
                    data-testid={`server-edit-${db.id}`}
                    onClick={() => onEdit(db)}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Edit</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 w-7 p-0"
                    aria-label={`Duplicate ${name}`}
                    data-testid={`server-duplicate-${db.id}`}
                    onClick={() => onDuplicate(db)}
                  >
                    <Copy className="h-3.5 w-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Duplicate</TooltipContent>
              </Tooltip>
              {serverAuthScopes[db.id] && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="sm"
                      className={`h-7 w-7 p-0 ${serverAuthScopes[db.id].has_secrets ? 'text-emerald-600 hover:bg-emerald-500/10' : 'text-amber-600 hover:bg-amber-500/10'}`}
                      aria-label={`API keys for ${name}`}
                      data-testid={`server-key-${db.id}`}
                      onClick={() => onApiKey(db, serverAuthScopes[db.id])}
                    >
                      <Key className="h-3.5 w-3.5" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>
                    {serverAuthScopes[db.id].has_secrets ? 'API Keys (configured)' : 'API Keys (needs setup)'}
                  </TooltipContent>
                </Tooltip>
              )}
              {db.transport === 'http' && db.url && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0 text-primary hover:bg-primary/10"
                      aria-label={`Connect ${name}`}
                      data-testid={`server-connect-${db.id}`}
                      onClick={() => onConnect(db)}
                    >
                      <Link className="h-3.5 w-3.5" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Connect</TooltipContent>
                </Tooltip>
              )}
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 w-7 p-0 hover:bg-destructive/10 hover:text-destructive"
                    aria-label={`Delete ${name}`}
                    data-testid={`server-delete-${db.id}`}
                    onClick={() => onDelete(db)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Delete</TooltipContent>
              </Tooltip>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
