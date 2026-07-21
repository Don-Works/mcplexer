import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { ChevronDown, ChevronRight, Pencil, Plus, Server, ShieldCheck, Trash2 } from 'lucide-react'
import type { AuthScope, DownstreamServer, RouteRule, Workspace } from '@/api/types'
import { cn } from '@/lib/utils'
import { scopeLabel } from '@/lib/scope-label'

interface RouteWorkspaceGroupProps {
  workspace: Workspace
  rules: RouteRule[]
  expanded: boolean
  onToggle: () => void
  onEnableServers: () => void
  onAddRule: () => void
  onEditRule: (rule: RouteRule) => void
  onDeleteRule: (rule: RouteRule) => void
  downstreams: DownstreamServer[]
  authScopes: AuthScope[]
  highlightRouteId?: string | null
  highlightWorkspace?: boolean
}

const MAX_BADGES = 5

export function RouteWorkspaceGroup({
  workspace,
  rules,
  expanded,
  onToggle,
  onEnableServers,
  onAddRule,
  onEditRule,
  onDeleteRule,
  downstreams,
  authScopes,
  highlightRouteId,
  highlightWorkspace,
}: RouteWorkspaceGroupProps) {
  const dsName = (id: string) => downstreams.find((d) => d.id === id)?.name ?? id
  const asName = (id: string) => {
    const scope = authScopes.find((item) => item.id === id)
    return scope ? scopeLabel(scope) : id
  }

  const enabledDownstreams = [...new Set(rules.map((r) => r.downstream_server_id).filter(Boolean))]

  return (
    <Card
      data-route-workspace-id={workspace.id}
      className={cn(
        'transition-shadow',
        highlightWorkspace && 'ring-2 ring-amber-400/80 ring-offset-2 ring-offset-background',
      )}
    >
      <div className="flex items-center gap-2">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/30"
          onClick={onToggle}
          aria-expanded={expanded}
        >
          {expanded ? (
            <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="truncate font-semibold">{workspace.name}</span>
              <Badge variant="secondary" className="shrink-0 text-xs">
                {rules.length} rule{rules.length !== 1 ? 's' : ''}
              </Badge>
            </div>
            {!expanded && enabledDownstreams.length > 0 && (
              <div className="mt-1 flex flex-wrap gap-1">
                {enabledDownstreams.slice(0, MAX_BADGES).map((dsId) => (
                  <Badge key={dsId} variant="outline" className="border-green-500/30 text-xs text-green-400">
                    {dsName(dsId)}
                  </Badge>
                ))}
                {enabledDownstreams.length > MAX_BADGES && (
                  <Badge variant="outline" className="text-xs text-muted-foreground">
                    +{enabledDownstreams.length - MAX_BADGES} more
                  </Badge>
                )}
              </div>
            )}
          </div>
        </button>
        <Button
          variant="outline"
          size="sm"
          className="mr-3 shrink-0"
          onClick={onEnableServers}
        >
          <Server className="mr-1.5 h-3.5 w-3.5" />
          Enable Servers
        </Button>
      </div>

      {expanded && (
        <CardContent className="pt-0 pb-4 px-4">
          {rules.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-8 text-muted-foreground">
              <Server className="mb-2 h-8 w-8 text-muted-foreground/50" />
              <p className="text-sm">No access rules</p>
              <button onClick={onEnableServers} className="text-xs text-primary hover:underline">
                Click Enable Servers to get started
              </button>
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow className="border-border/50 hover:bg-transparent">
                    <TableHead className="hidden sm:table-cell">Priority</TableHead>
                    <TableHead>Name</TableHead>
                    <TableHead className="hidden md:table-cell">Path Glob</TableHead>
                    <TableHead className="hidden lg:table-cell">Downstream</TableHead>
                    <TableHead className="hidden lg:table-cell">Credential</TableHead>
                    <TableHead className="hidden lg:table-cell">Approval</TableHead>
                    <TableHead>Policy</TableHead>
                    <TableHead className="w-24">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rules.map((r) => (
                    <TableRow
                      key={r.id}
                      data-route-id={r.id}
                      className={
                        'border-border/30 hover:bg-muted/30 ' +
                        (highlightRouteId === r.id ? 'ring-2 ring-amber-400/80 ring-offset-2 ring-offset-background' : '')
                      }
                    >
                      <TableCell className="hidden sm:table-cell font-mono text-sm text-muted-foreground">
                        {r.priority}
                      </TableCell>
                      <TableCell className="text-sm">
                        {r.name || <span className="text-muted-foreground/40">&mdash;</span>}
                      </TableCell>
                      <TableCell className="hidden md:table-cell">
                        <div className="max-w-[14rem] truncate font-mono text-xs text-accent-foreground" title={r.path_glob}>
                          {r.path_glob}
                        </div>
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        <div className="max-w-[14rem] truncate" title={dsName(r.downstream_server_id)}>{dsName(r.downstream_server_id)}</div>
                      </TableCell>
                      <TableCell className="hidden lg:table-cell text-muted-foreground">
                        <div className="max-w-[14rem] truncate" title={r.auth_scope_id ? asName(r.auth_scope_id) : undefined}>
                          {r.auth_scope_id ? asName(r.auth_scope_id) : '-'}
                        </div>
                      </TableCell>
                      <TableCell className="hidden lg:table-cell">
                        {r.approval_mode === 'all' ? (
                          <Badge variant="outline" className="gap-1 text-amber-400 border-amber-500/30">
                            <ShieldCheck className="h-3 w-3" />
                            all
                          </Badge>
                        ) : r.approval_mode === 'write' ? (
                          <Badge variant="outline" className="gap-1 text-yellow-500 border-yellow-500/30">
                            <ShieldCheck className="h-3 w-3" />
                            write only
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground/40">-</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant={r.policy === 'allow' ? 'secondary' : 'destructive'}>
                          {r.policy}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8 w-8 p-0"
                                aria-label={`Edit route ${r.name || 'rule'}`}
                                onClick={() => onEditRule(r)}
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
                                className="h-8 w-8 p-0 hover:bg-destructive/10 hover:text-destructive"
                                aria-label={`Delete route ${r.name || 'rule'}`}
                                onClick={() => onDeleteRule(r)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Delete</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <div className="mt-3 flex justify-end">
                <Button variant="ghost" size="sm" onClick={onAddRule}>
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  Add access rule
                </Button>
              </div>
            </>
          )}
        </CardContent>
      )}
    </Card>
  )
}
