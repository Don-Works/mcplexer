import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { SessionInfo } from '@/api/types'
import { formatDuration } from './chart-components'
import { useNavigate } from 'react-router-dom'

export function ActiveSessionsTable({
  sessions,
  wsName,
}: {
  sessions: SessionInfo[]
  wsName: (id: string) => string
}) {
  const navigate = useNavigate()
  const displayLimit = 10
  const shown = sessions.slice(0, displayLimit)
  const remaining = sessions.length - shown.length
  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
            Active Sessions
          </CardTitle>
          <span className="text-xs text-muted-foreground font-mono">{sessions.length} total</span>
        </div>
      </CardHeader>
      <CardContent>
        {sessions.length === 0 ? (
          <div className="py-6 text-center text-xs text-muted-foreground/60">
            No active sessions
          </div>
        ) : (
        <>
        <Table>
          <TableHeader>
            <TableRow className="border-border/50 hover:bg-transparent">
              <TableHead>Session</TableHead>
              <TableHead>Client</TableHead>
              <TableHead className="hidden sm:table-cell">Workspace</TableHead>
              <TableHead className="hidden md:table-cell text-right">PID</TableHead>
              <TableHead className="text-right">Connected</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((s) => (
              <TableRow key={s.id} className="border-border/30">
                <TableCell>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <button
                        className="font-mono text-xs text-cyan-400 hover:text-cyan-300 hover:underline"
                        onClick={() => navigate(`/audit?session_id=${s.id}`)}
                      >
                        {s.id.slice(0, 8)}
                      </button>
                    </TooltipTrigger>
                    <TooltipContent>View audit log for this session</TooltipContent>
                  </Tooltip>
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {s.client_type}{s.model_hint ? <span className="ml-1 text-xs text-muted-foreground/60">({s.model_hint})</span> : null}
                </TableCell>
                <TableCell className="hidden sm:table-cell text-sm text-muted-foreground">
                  {s.workspace_id ? wsName(s.workspace_id) : '-'}
                </TableCell>
                <TableCell className="hidden md:table-cell text-right font-mono text-xs text-muted-foreground">
                  {s.client_pid ?? '-'}
                </TableCell>
                <TableCell className="text-right font-mono text-sm text-muted-foreground">
                  {formatDuration(s.connected_at)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {remaining > 0 && (
          <div className="mt-2 text-center text-xs text-muted-foreground">
            +{remaining} more session{remaining !== 1 ? 's' : ''}
          </div>
        )}
        </>
        )}
      </CardContent>
    </Card>
  )
}
