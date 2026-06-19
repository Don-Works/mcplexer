import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { RouteHitEntry } from '@/api/types'

export function RouteHitMapTable({ entries }: { entries: RouteHitEntry[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
          Route Hits
        </CardTitle>
      </CardHeader>
      <CardContent>
        {entries.length === 0 ? (
          <div className="py-6 text-center text-xs text-muted-foreground/60">
            No route hits in this period
          </div>
        ) : (
        <Table>
          <TableHeader>
            <TableRow className="border-border/50 hover:bg-transparent">
              <TableHead>Rule</TableHead>
              <TableHead className="text-right">Hits</TableHead>
              <TableHead className="text-right">Errors</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {entries.map((e) => (
              <TableRow key={e.route_rule_id} className="border-border/30">
                <TableCell>
                  <div className="font-mono text-sm">{e.rule_name || e.path_glob || e.route_rule_id}</div>
                  {e.rule_name && e.path_glob && (
                    <div className="text-xs text-muted-foreground font-mono">{e.path_glob}</div>
                  )}
                </TableCell>
                <TableCell className="text-right font-mono text-sm">{e.hit_count}</TableCell>
                <TableCell className="text-right font-mono text-sm text-destructive">
                  {e.error_count > 0 ? e.error_count : '-'}
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
