import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { AuditFilter, AuditRecord } from '@/api/types'
import { AuditTable } from '@/components/audit/AuditTable'
import { useNavigate } from 'react-router-dom'

function isBlocked(record: AuditRecord): boolean {
  return record.status === 'blocked'
}

// The dashboard tiles share one compact column set: drop session / client /
// cache / group so the table stays tight on the home grid. Session + execution
// remain reachable via the inspector the row click opens, and their badges are
// still clickable through the onFilter deep-link below.
const DASH_COLUMNS = {
  timestamp: true,
  tool: true,
  workspace: true,
  status: true,
  reason: true,
  latency: true,
  session: false,
  client: false,
  cache: false,
  group: false,
} as const

// A compact variant for the errors tile: no Workspace / Client / Cache and no
// Latency (a failed call's latency is noise), but KEEP Session + Group so their
// clickable badges still deep-link to /audit, matching the bespoke errors rows.
const ERROR_COLUMNS = {
  timestamp: true,
  tool: true,
  status: true,
  reason: true,
  session: true,
  group: true,
  workspace: false,
  client: false,
  cache: false,
  latency: false,
} as const

// Clickable session / execution badges deep-link into the audit page with the
// matching facet pre-applied: the same targets the bespoke rows used to
// navigate to, now driven through AuditRow's onFilter contract.
function navFilterFor(navigate: (to: string) => void) {
  return (patch: Partial<AuditFilter>) => {
    if (patch.session_id) navigate(`/audit?session_id=${patch.session_id}`)
    else if (patch.execution_id) navigate(`/audit?execution_id=${patch.execution_id}`)
  }
}

export function RecentCallsTable({
  recentCalls,
  connected,
  wsName,
  onSelect,
}: {
  recentCalls: AuditRecord[]
  connected: boolean
  wsName: (id: string) => string
  onSelect: (record: AuditRecord) => void
}) {
  const navigate = useNavigate()
  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
            Recent Calls
          </CardTitle>
          <div className="flex items-center gap-2 text-xs">
            {connected ? (
              <>
                <span className="relative flex h-2 w-2">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                  <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-500" />
                </span>
                <span className="text-emerald-400">Live</span>
              </>
            ) : (
              <span className="text-muted-foreground">Connecting...</span>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {recentCalls.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
            <div className="w-full max-w-xs rounded-lg border border-border/30 bg-muted/30 font-mono text-sm">
              <div className="flex items-center gap-1.5 border-b border-border/20 px-3 py-1.5">
                <span className="h-2 w-2 rounded-full bg-muted-foreground/20" />
                <span className="h-2 w-2 rounded-full bg-muted-foreground/20" />
                <span className="h-2 w-2 rounded-full bg-muted-foreground/20" />
                <span className="ml-2 text-[10px] text-muted-foreground/40">mcplexer</span>
              </div>
              <div className="space-y-1 px-3 py-3">
                <p className="text-muted-foreground/40">$ mcplexer serve --mode=stdio</p>
                <p className="text-muted-foreground/50">listening for tool calls...</p>
                <p>
                  <span className="text-primary">$</span>
                  <span className="ml-1 inline-block h-3.5 w-1.5 translate-y-[1px] animate-pulse bg-primary/70" />
                </p>
              </div>
            </div>
            <p className="mt-4 text-xs text-muted-foreground/60">
              Tool calls will appear here once sessions are active
            </p>
          </div>
        ) : (
          <AuditTable
            records={recentCalls}
            columns={DASH_COLUMNS}
            liveCount={1}
            wsName={wsName}
            onSelect={onSelect}
            onFilter={navFilterFor(navigate)}
          />
        )}
      </CardContent>
    </Card>
  )
}

export function RecentErrorsTable({
  recentErrors,
  onSelect,
}: {
  recentErrors: AuditRecord[]
  onSelect: (record: AuditRecord) => void
}) {
  const navigate = useNavigate()
  return (
    <Card className="border-destructive/30">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm font-medium uppercase tracking-wider text-destructive">
            Recent Errors & Blocked
          </CardTitle>
          <div className="flex gap-3 text-xs text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <span className="h-2 w-2 rounded-full bg-destructive" />
              {recentErrors.filter((e) => !isBlocked(e)).length} errors
            </span>
            <span className="flex items-center gap-1.5">
              <span className="h-2 w-2 rounded-full bg-amber-500" />
              {recentErrors.filter((e) => isBlocked(e)).length} blocked
            </span>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <AuditTable
          records={recentErrors}
          columns={ERROR_COLUMNS}
          emptyHint="Errors and blocked calls will appear here"
          onSelect={onSelect}
          onFilter={navFilterFor(navigate)}
        />
      </CardContent>
    </Card>
  )
}
