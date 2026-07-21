import { Badge } from '@/components/ui/badge'
import type { DownstreamOAuthStatusEntry, DownstreamServer } from '@/api/types'
import { AlertCircle, Clock } from 'lucide-react'
import { formatRelativeTime } from '@/lib/utils'

export function getOAuthBadges(
  ds: DownstreamServer,
  oauthStatuses: Record<string, DownstreamOAuthStatusEntry[]>,
  statusErrors: Record<string, boolean>,
  onConnect: (ds: DownstreamServer) => void,
) {
  if (ds.transport !== 'http') return null
  if (statusErrors[ds.id]) {
    return (
      <Badge variant="outline" className="text-xs text-destructive border-destructive/30">
        <AlertCircle className="mr-1 h-3 w-3" /> Error
      </Badge>
    )
  }
  const entries = oauthStatuses[ds.id]
  if (!entries || entries.length === 0) {
    return (
      <Badge variant="outline" className="text-xs text-muted-foreground cursor-pointer hover:text-primary hover:border-primary"
        onClick={(e) => { e.stopPropagation(); onConnect(ds) }}>
        Not Connected
      </Badge>
    )
  }
  return (
    <div className="flex flex-col gap-1 min-w-0">
      {entries.map((entry) => {
        const expiring = entry.expires_at && (new Date(entry.expires_at).getTime() - Date.now()) < 7 * 24 * 60 * 60 * 1000
        if (entry.status === 'authenticated') {
          return (
            <Badge
              key={entry.auth_scope_id}
              className={`cursor-pointer text-xs border-0 hover:opacity-80 max-w-full truncate ${expiring ? 'bg-amber-500/15 text-amber-600' : 'bg-emerald-500/15 text-emerald-600'}`}
              onClick={(e) => {
                e.stopPropagation()
                onConnect(ds)
              }}
            >
              {entry.auth_scope_name}
              {entry.expires_at && (
                <span className="ml-1 shrink-0 opacity-70">
                  <Clock className="mr-0.5 inline h-2.5 w-2.5" />
                  {formatRelativeTime(entry.expires_at)}
                </span>
              )}
            </Badge>
          )
        }
        if (entry.status === 'expired') {
          return (
            <Badge
              key={entry.auth_scope_id}
              variant="outline"
              className="cursor-pointer text-xs text-amber-600 border-amber-300 hover:border-amber-500 max-w-full truncate"
              onClick={(e) => {
                e.stopPropagation()
                onConnect(ds)
              }}
            >
              {entry.auth_scope_name} — Expired
            </Badge>
          )
        }
        return (
          <Badge
            key={entry.auth_scope_id}
            variant="outline"
            className="cursor-pointer text-xs text-muted-foreground hover:text-primary hover:border-primary max-w-full truncate"
            onClick={(e) => {
              e.stopPropagation()
              onConnect(ds)
            }}
          >
            {entry.auth_scope_name}
          </Badge>
        )
      })}
    </div>
  )
}
