import { useCallback } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import { getGuardsOverview } from '@/api/client'
import type { GuardsOverview } from '@/api/client'
import {
  Activity,
  Box,
  CalendarClock,
  ChevronRight,
  Filter,
  Loader2,
  ShieldAlert,
  Terminal,
} from 'lucide-react'

// GuardsOverviewPage — landing page for /guards. One card per Guard
// with at-a-glance counters; each card links to its detail subpage.
// The five Guards stay in a fixed order matching the sidebar (Shell,
// Sanitizer, Schedule, Sandbox, MCP) so muscle memory transfers
// between the two surfaces.
export function GuardsOverviewPage() {
  const fetcher = useCallback(() => getGuardsOverview(), [])
  const { data, loading, error } = useApi(fetcher)

  return (
    <div className="space-y-5 max-w-5xl">
      <div>
        <h1 className="text-2xl font-bold">Guards</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The five enforcement layers that gate what your agents can do. Each
          Guard has its own toggles and an audit trail; this page is the
          at-a-glance dashboard.
        </p>
      </div>

      {loading && !data ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading Guards status…
        </div>
      ) : error ? (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      ) : data ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <ShellCard data={data} />
          <SanitizerCard data={data} />
          <ScheduleCard data={data} />
          <SandboxCard data={data} />
          <MCPCard data={data} />
        </div>
      ) : null}
    </div>
  )
}

interface CardWrapProps {
  to: string
  icon: React.ReactNode
  title: string
  children: React.ReactNode
}

function GuardCard({ to, icon, title, children }: CardWrapProps) {
  return (
    <Link to={to} className="group">
      <Card className="h-full transition-colors hover:border-primary/40">
        <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
          <CardTitle className="flex items-center gap-2 text-sm font-medium">
            <span className="text-muted-foreground group-hover:text-primary">
              {icon}
            </span>
            {title}
          </CardTitle>
          <ChevronRight className="h-4 w-4 text-muted-foreground/40 transition-colors group-hover:text-primary" />
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </Link>
  )
}

function ShellCard({ data }: { data: GuardsOverview }) {
  const { hooks_installed_count, hooks_total_clients, recent_denied_count_24h } = data.shell
  return (
    <GuardCard to="/guards/shell" icon={<Terminal className="h-4 w-4" />} title="Shell Guard">
      <div className="text-2xl font-semibold">
        {hooks_installed_count}
        <span className="text-base font-normal text-muted-foreground">
          /{hooks_total_clients}
        </span>
      </div>
      <p className="text-xs text-muted-foreground">cooperative hooks installed</p>
      {recent_denied_count_24h > 0 && (
        <div className="mt-2 inline-flex items-center gap-1.5 text-xs text-amber-600">
          <ShieldAlert className="h-3 w-3" />
          {recent_denied_count_24h} denied in last 24h
        </div>
      )}
    </GuardCard>
  )
}

function SanitizerCard({ data }: { data: GuardsOverview }) {
  const { denylist_size, detected_count_24h, envelope_always } = data.sanitizer
  return (
    <GuardCard to="/guards/sanitizer" icon={<Filter className="h-4 w-4" />} title="Sanitizer Guard">
      <div className="text-2xl font-semibold">{detected_count_24h}</div>
      <p className="text-xs text-muted-foreground">injection patterns matched (24h)</p>
      <div className="mt-2 flex flex-wrap gap-1.5">
        <Badge variant="secondary" className="text-[10px]">
          {denylist_size} denylist rules
        </Badge>
        {envelope_always && (
          <Badge className="bg-emerald-500/10 text-emerald-400 border-emerald-500/30 text-[10px]">
            always envelope
          </Badge>
        )}
      </div>
    </GuardCard>
  )
}

function ScheduleCard({ data }: { data: GuardsOverview }) {
  const { jobs_total, jobs_ran_24h } = data.schedule
  return (
    <GuardCard to="/guards/schedule" icon={<CalendarClock className="h-4 w-4" />} title="Schedule Guard">
      <div className="text-2xl font-semibold">{jobs_total}</div>
      <p className="text-xs text-muted-foreground">jobs scheduled</p>
      <div className="mt-2 inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        <Activity className="h-3 w-3" />
        {jobs_ran_24h} ran in last 24h
      </div>
    </GuardCard>
  )
}

function SandboxCard({ data }: { data: GuardsOverview }) {
  const { driver, unsupported_os } = data.sandbox
  // Server returns null for an empty slice (Go nil-slice → JSON null);
  // coalesce defensively so .filter()/.length never crash the overview.
  const clients = data.sandbox.clients ?? []
  const enabledCount = clients.filter((c) => c.enabled).length
  return (
    <GuardCard to="/guards/sandbox" icon={<Box className="h-4 w-4" />} title="Sandbox Guard">
      <div className="text-2xl font-semibold">
        {enabledCount}
        <span className="text-base font-normal text-muted-foreground">/{clients.length}</span>
      </div>
      <p className="text-xs text-muted-foreground">clients sandboxed</p>
      <div className="mt-2">
        {unsupported_os ? (
          <Badge variant="outline" className="text-[10px] text-muted-foreground">
            unsupported OS
          </Badge>
        ) : driver ? (
          <Badge variant="secondary" className="font-mono text-[10px]">
            {driver}
          </Badge>
        ) : (
          <Badge variant="outline" className="text-[10px] text-muted-foreground">
            no driver
          </Badge>
        )}
      </div>
    </GuardCard>
  )
}

function MCPCard({ data }: { data: GuardsOverview }) {
  const { downstream_count, route_count } = data.mcp
  return (
    <GuardCard to="/workspaces" icon={<ChevronRight className="h-4 w-4" />} title="MCP Guard">
      <div className="text-2xl font-semibold">{downstream_count}</div>
      <p className="text-xs text-muted-foreground">downstream servers</p>
      <div className="mt-2 inline-flex items-center gap-1.5 text-xs text-muted-foreground">
        {route_count} active routes
      </div>
    </GuardCard>
  )
}
