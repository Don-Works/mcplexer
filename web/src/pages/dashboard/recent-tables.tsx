import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { AuditRecord } from '@/api/types'
import { ReasonBadge, SecretEventBadge } from '@/components/AuditDetailDialog'
import { classifySecretEvent, normalizeStatus } from '@/lib/audit-semantics'
import { formatTime } from './chart-components'
import { Layers, Monitor } from 'lucide-react'
import { useNavigate } from 'react-router-dom'

function isBlocked(record: AuditRecord): boolean {
  return record.status === 'blocked'
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
          <Table className="table-fixed">
            <colgroup>
              <col className="w-[5rem]" />
              <col className="w-[18rem]" />
              <col className="hidden md:table-column w-[10rem]" />
              <col className="w-[6rem]" />
              <col />
              <col className="hidden lg:table-column w-[8rem]" />
              <col className="hidden lg:table-column w-[8rem]" />
              <col className="hidden sm:table-column w-[5rem]" />
            </colgroup>
            <TableHeader>
              <TableRow className="border-border/50 hover:bg-transparent">
                <TableHead>Time</TableHead>
                <TableHead>Tool</TableHead>
                <TableHead className="hidden md:table-cell">Workspace</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Reason</TableHead>
                <TableHead className="hidden lg:table-cell">Session</TableHead>
                <TableHead className="hidden lg:table-cell">Execution</TableHead>
                <TableHead className="hidden sm:table-cell text-right">Latency</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {recentCalls.map((call, idx) => (
                <TableRow
                  key={call.id}
                  className={`cursor-pointer border-border/30 hover:bg-muted/30 ${
                    idx === 0 ? 'animate-[audit-in_0.3s_ease-out]' : ''
                  }`}
                  onClick={() => onSelect(call)}
                >
                  <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">
                    {formatTime(call.timestamp)}
                  </TableCell>
                  <TableCell>
                    <div className="max-w-[20rem] truncate font-mono text-sm text-accent-foreground" title={call.tool_name}>
                      {call.tool_name}
                    </div>
                    {classifySecretEvent(call.tool_name) && (
                      <div className="mt-1">
                        <SecretEventBadge toolName={call.tool_name} />
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="hidden md:table-cell text-muted-foreground">
                    <div className="max-w-[10rem] truncate">{call.workspace_name || (call.workspace_id ? wsName(call.workspace_id) : '-')}</div>
                  </TableCell>
                  <TableCell>
                    {(() => {
                      const tone = normalizeStatus(call.status)
                      return (
                        <Badge
                          variant={tone === 'success' ? 'secondary' : tone === 'blocked' ? 'outline' : 'destructive'}
                          className={tone === 'blocked' ? 'border-amber-500/40 text-amber-500' : ''}
                        >
                          {call.status}
                        </Badge>
                      )
                    })()}
                  </TableCell>
                  <TableCell className="align-top">
                    <ReasonBadge record={call} />
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    {call.session_id && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Badge
                            variant="outline"
                            className="cursor-pointer border-cyan-500/40 text-cyan-400 hover:bg-cyan-500/10"
                            onClick={(e) => {
                              e.stopPropagation()
                              navigate(`/audit?session_id=${call.session_id}`)
                            }}
                          >
                            <Monitor className="mr-1 h-3 w-3" />
                            {call.session_id.slice(0, 8)}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent>View all calls in this session</TooltipContent>
                      </Tooltip>
                    )}
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    {call.execution_id && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Badge
                            variant="outline"
                            className="cursor-pointer border-violet-500/40 text-violet-400 hover:bg-violet-500/10"
                            onClick={(e) => {
                              e.stopPropagation()
                              navigate(`/audit?execution_id=${call.execution_id}`)
                            }}
                          >
                            <Layers className="mr-1 h-3 w-3" />
                            {call.execution_id.slice(0, 8)}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent>View all calls in this execution</TooltipContent>
                      </Tooltip>
                    )}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell text-right font-mono text-sm text-muted-foreground">
                    {call.latency_ms}ms
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
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
        <Table className="table-fixed">
          <colgroup>
            <col className="w-[5rem]" />
            <col className="w-[18rem]" />
            <col className="w-[5rem]" />
            <col />
            <col className="hidden lg:table-column w-[8rem]" />
            <col className="hidden lg:table-column w-[8rem]" />
          </colgroup>
          <TableHeader>
            <TableRow className="border-border/50 hover:bg-transparent">
              <TableHead>Time</TableHead>
              <TableHead>Tool</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead className="hidden lg:table-cell">Session</TableHead>
              <TableHead className="hidden lg:table-cell">Execution</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {recentErrors.map((err) => {
              const blocked = isBlocked(err)
              return (
                <TableRow
                  key={err.id}
                  className={`cursor-pointer border-border/30 ${
                    blocked ? 'hover:bg-amber-500/5' : 'hover:bg-destructive/5'
                  }`}
                  onClick={() => onSelect(err)}
                >
                  <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">
                    {formatTime(err.timestamp)}
                  </TableCell>
                  <TableCell>
                    <div className="max-w-[20rem] truncate font-mono text-sm text-accent-foreground" title={err.tool_name}>
                      {err.tool_name}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={blocked
                        ? 'border-amber-500/40 text-amber-500'
                        : 'border-destructive/40 text-destructive'
                      }
                    >
                      {blocked ? 'blocked' : 'error'}
                    </Badge>
                  </TableCell>
                  <TableCell className="align-top">
                    <ReasonBadge record={err} />
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    {err.session_id && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Badge
                            variant="outline"
                            className="cursor-pointer border-cyan-500/40 text-cyan-400 hover:bg-cyan-500/10"
                            onClick={(e) => {
                              e.stopPropagation()
                              navigate(`/audit?session_id=${err.session_id}`)
                            }}
                          >
                            <Monitor className="mr-1 h-3 w-3" />
                            {err.session_id.slice(0, 8)}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent>View all calls in this session</TooltipContent>
                      </Tooltip>
                    )}
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    {err.execution_id && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Badge
                            variant="outline"
                            className="cursor-pointer border-violet-500/40 text-violet-400 hover:bg-violet-500/10"
                            onClick={(e) => {
                              e.stopPropagation()
                              navigate(`/audit?execution_id=${err.execution_id}`)
                            }}
                          >
                            <Layers className="mr-1 h-3 w-3" />
                            {err.execution_id.slice(0, 8)}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent>View all calls in this execution</TooltipContent>
                      </Tooltip>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}
