// MemoryTopEntities — landing-page tile listing the most-linked entities
// (migration 076). Each row is a Link to /memory/about/:kind/:id so the
// user can drill into "everything about Alice" with one click.
//
// Empty state explains how to start linking. Loading state matches the
// other landing tiles' rhythm.

import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowUpRight, Sparkles } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { Badge } from '@/components/ui/badge'
import { listEntities, type EntitySummary } from '@/api/memory'
import { relativeTime } from './memory-utils'

const TOP_N = 8

export function MemoryTopEntities() {
  const [rows, setRows] = useState<EntitySummary[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listEntities({ limit: TOP_N })
      .then((r) => {
        if (!cancelled) setRows(r)
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Load failed')
          setRows([])
        }
      })
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <Card className="overflow-hidden">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Sparkles className="h-4 w-4 text-primary" />
            <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              Top entities
            </h2>
          </div>
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
            by memory count
          </span>
        </div>
        {error && <p className="text-destructive text-xs">Error: {error}</p>}
        {rows && rows.length === 0 && !error && (
          <EmptyState
            icon={<Sparkles className="h-7 w-7" />}
            title="No entities yet"
            description={
              'When a memory is saved with entities=[{kind,id}] — e.g. linked ' +
              'to a task, person, place, or peer — it shows up here. The ' +
              'consolidator can also extract entity links from notes if you ' +
              'enable that pass.'
            }
            density="card"
            testid="memory-top-entities-empty"
          />
        )}
        {rows && rows.length > 0 && (
          <ul className="divide-y divide-border/30 border border-border/40 bg-background/40">
            {rows.map((e) => (
              <li
                key={`${e.kind}:${e.id}`}
                className="group flex items-center justify-between px-3 py-2.5"
              >
                <Link
                  to={`/memory/about/${encodeURIComponent(e.kind)}/${encodeURIComponent(e.id)}`}
                  className="flex min-w-0 flex-1 items-center gap-2"
                >
                  <Badge
                    variant="outline"
                    tone="muted"
                    className="font-mono text-[9px] uppercase"
                  >
                    {e.kind}
                  </Badge>
                  <span className="truncate text-[13px] font-medium text-foreground group-hover:text-primary">
                    {e.id}
                  </span>
                </Link>
                <div className="ml-3 flex shrink-0 items-center gap-3">
                  <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                    {e.memory_count}
                    <span className="text-muted-foreground/60">
                      {' '}
                      memor{e.memory_count === 1 ? 'y' : 'ies'}
                    </span>
                  </span>
                  <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
                    {relativeTime(e.last_linked_at)}
                  </span>
                  <ArrowUpRight className="h-3 w-3 text-muted-foreground/40 transition-colors group-hover:text-foreground" />
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
