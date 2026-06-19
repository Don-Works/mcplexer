// MemoryLandingTiles — presentational tiles + activity helpers used by
// MemoryLandingPage. Extracted to keep the page file under the 300-line
// guideline.

import { Link } from 'react-router-dom'
import { ArrowUpRight, Brain, Sparkles } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { CopyButton } from '@/components/ui/copy-button'
import { EmptyState } from '@/components/ui/empty-state'
import { Badge } from '@/components/ui/badge'
import type { StoredNotification } from '@/api/notifications'
import { cn } from '@/lib/utils'
import { relativeTime } from './memory-utils'

export function VitalsTile({
  icon,
  label,
  value,
  detail,
  href,
  dim,
  accent,
}: {
  icon: React.ReactNode
  label: string
  value: string
  detail: string
  href: string
  dim?: boolean
  accent?: 'awaiting' | 'idle'
}) {
  return (
    <Link
      to={href}
      className={cn(
        'group block border border-border bg-card/40 px-4 py-3.5 transition-colors',
        'hover:border-border/80 hover:bg-card focus-visible:outline-none focus-visible:border-primary/60',
      )}
    >
      <div className="flex items-center justify-between text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          {icon}
          {label}
        </span>
        <ArrowUpRight className="h-3 w-3 opacity-0 transition-opacity group-hover:opacity-100" />
      </div>
      <div
        className={cn(
          'mt-2 font-mono text-3xl font-semibold tracking-tight tabular-nums',
          dim ? 'text-muted-foreground/70' : 'text-foreground',
          accent === 'awaiting' && 'text-amber-300',
        )}
      >
        {value}
      </div>
      <div
        className={cn(
          'mt-1 flex items-center gap-1.5 text-[11px]',
          accent === 'awaiting' ? 'text-amber-300/80' : 'text-muted-foreground',
        )}
      >
        {accent === 'awaiting' && (
          <span className="relative flex h-1.5 w-1.5">
            <span className="absolute inline-flex h-full w-full animate-pulse-slow rounded-full bg-amber-400/60" />
            <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-amber-400" />
          </span>
        )}
        {detail}
      </div>
    </Link>
  )
}

export function ActivityCard({ events }: { events: StoredNotification[] }) {
  return (
    <Card className="overflow-hidden">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Sparkles className="h-4 w-4 text-primary" />
            <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
              Live activity
            </h2>
          </div>
          <Link
            to="/memory/activity"
            className="inline-flex items-center gap-1 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/70 transition-colors hover:text-foreground"
          >
            Full timeline
            <ArrowUpRight className="h-3 w-3" />
          </Link>
        </div>
        {events.length === 0 ? (
          <EmptyState
            icon={<Brain className="h-7 w-7" />}
            title="Nothing learned yet"
            description="When an agent writes a memory, accepts a peer offer, or completes a consolidation pass, you will see it here in real-time."
            density="card"
            testid="memory-activity-empty"
          />
        ) : (
          <ul className="divide-y divide-border/30 border border-border/40 bg-background/40">
            {events.map((e) => (
              <ActivityRow key={`${e.id}-${e.message_id}`} event={e} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function ActivityRow({ event }: { event: StoredNotification }) {
  const kindLabel = humanizeKind(event.kind)
  const body = (
    <div className="flex items-start gap-3 px-3 py-2.5 transition-colors hover:bg-muted/20">
      <span
        className={cn(
          'mt-1 inline-flex h-1.5 w-1.5 shrink-0 rounded-full',
          event.priority === 'critical'
            ? 'bg-red-400'
            : event.priority === 'high'
              ? 'bg-orange-400'
              : 'bg-emerald-400/80',
        )}
      />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[13px] font-medium text-foreground">
            {event.title}
          </span>
          <Badge variant="outline" tone="muted" className="font-mono text-[9px] uppercase">
            {kindLabel}
          </Badge>
          {event.link && (
            <ArrowUpRight className="h-3 w-3 shrink-0 text-muted-foreground/40" />
          )}
        </div>
        {event.body && (
          <p className="mt-0.5 truncate text-[11.5px] text-muted-foreground/90">
            {event.body}
          </p>
        )}
      </div>
      <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
        {relativeTime(event.created_at)}
      </span>
    </div>
  )
  if (event.link) {
    return (
      <li>
        <Link to={event.link} className="block">
          {body}
        </Link>
      </li>
    )
  }
  return (
    <li>
      {body}
    </li>
  )
}

function humanizeKind(kind: string): string {
  if (!kind) return 'event'
  return kind.replace(/^memory[._]/i, '').replace(/_/g, ' ') || 'event'
}

export function HarnessImportCard() {
  const importLine = '@~/.mcplexer/memory-exports/global.md'
  return (
    <Card className="flex flex-col">
      <CardContent className="flex flex-1 flex-col gap-3 p-4">
        <div>
          <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
            Harness integration
          </h2>
          <p className="mt-2 text-[12px] leading-relaxed text-muted-foreground/90">
            Wire your memories into every agent session. Add this line to
            your <code className="font-mono text-foreground/80">CLAUDE.md</code> or equivalent harness config
            and the gateway will keep an up-to-date markdown export pinned for the agent to read on every turn.
          </p>
        </div>
        <div className="group/import flex items-stretch border border-border bg-background/60">
          <code className="flex-1 truncate px-3 py-2.5 font-mono text-[12.5px] text-emerald-300">
            {importLine}
          </code>
          <CopyButton
            value={importLine}
            className="self-stretch px-2 text-muted-foreground"
          />
        </div>
        <p className="mt-auto text-[11px] text-muted-foreground/60">
          Tip: workspace-scoped memories export to <span className="font-mono">workspace-&lt;id&gt;.md</span> if you
          want narrower context per project.
        </p>
      </CardContent>
    </Card>
  )
}

export function QuickLink({
  to,
  title,
  body,
  accent,
}: {
  to: string
  title: string
  body: string
  accent?: boolean
}) {
  return (
    <Link
      to={to}
      className={cn(
        'group flex flex-col gap-1 border border-border bg-card/40 px-4 py-3 transition-colors',
        'hover:border-border/80 hover:bg-card',
        accent && 'border-amber-500/40',
      )}
    >
      <div className="flex items-center justify-between">
        <span
          className={cn(
            'text-[13px] font-semibold',
            accent ? 'text-amber-300' : 'text-foreground',
          )}
        >
          {title}
        </span>
        <ArrowUpRight className="h-3.5 w-3.5 text-muted-foreground/50 transition-colors group-hover:text-foreground" />
      </div>
      <p className="text-[11.5px] leading-relaxed text-muted-foreground">
        {body}
      </p>
    </Link>
  )
}
