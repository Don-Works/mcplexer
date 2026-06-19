import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { ToolLeaderboardEntry } from '@/api/types'

function errorRateColor(rate: number): string {
  if (rate < 5) return 'text-chart-2'
  if (rate < 20) return 'text-amber-400'
  return 'text-destructive'
}

export function ToolLeaderboardTable({ entries }: { entries: ToolLeaderboardEntry[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
          Tool Leaderboard
        </CardTitle>
      </CardHeader>
      <CardContent>
        {entries.length === 0 ? (
          <div className="py-6 text-center text-xs text-muted-foreground/60">
            No tool calls in this period
          </div>
        ) : (
        <Table>
          <TableHeader>
            <TableRow className="border-border/50 hover:bg-transparent">
              <TableHead>Tool</TableHead>
              <TableHead className="text-right">Calls</TableHead>
              <TableHead className="text-right">Errors</TableHead>
              <TableHead className="text-right">Error %</TableHead>
              <TableHead className="hidden sm:table-cell text-right">Avg Lat.</TableHead>
              <TableHead className="hidden md:table-cell text-right">P95 Lat.</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {entries.map((e) => (
              <TableRow key={e.tool_name} className="border-border/30">
                <TableCell>
                  <div className="max-w-[20rem] truncate font-mono text-sm" title={e.tool_name}>{e.tool_name}</div>
                  {e.server_name && (
                    <div className="text-xs text-muted-foreground">{e.server_name}</div>
                  )}
                </TableCell>
                <TableCell className="text-right font-mono text-sm">{e.call_count}</TableCell>
                <TableCell className="text-right font-mono text-sm">{e.error_count}</TableCell>
                <TableCell className={`text-right font-mono text-sm ${errorRateColor(e.error_rate)}`}>
                  {e.error_rate.toFixed(1)}%
                </TableCell>
                <TableCell className="hidden sm:table-cell text-right font-mono text-sm text-muted-foreground">
                  {Math.round(e.avg_latency_ms)}ms
                </TableCell>
                <TableCell className="hidden md:table-cell text-right font-mono text-sm text-muted-foreground">
                  {e.p95_latency_ms}ms
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
