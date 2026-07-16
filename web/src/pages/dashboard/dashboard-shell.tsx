// dashboard-shell — header strip + first-run welcome + loading/error
// chrome. Extracted from DashboardPage so the orchestrator file stays
// under 300 lines and the header doesn't need to round-trip through
// state it doesn't use.

import { Link } from 'react-router-dom'
import { Activity, Server } from 'lucide-react'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { ConfigureWithAI } from '@/components/ConfigureWithAI'
import { type TimeRange } from './chart-components'

interface DashState {
  state: 'quiet' | 'busy' | 'trouble'
  range: TimeRange
  setRange: (r: TimeRange) => void
  ranges: TimeRange[]
}

export function DashboardHeader({ state, range, setRange, ranges }: DashState) {
  return (
    <div className="flex items-center justify-between">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-bold tracking-tight">Dashboard</h1>
        <StateTag state={state} />
      </div>
      <ToggleGroup
        type="single"
        value={range}
        onValueChange={(v) => {
          if (v) setRange(v as TimeRange)
        }}
        variant="outline"
        size="sm"
        aria-label="Dashboard time range"
      >
        {ranges.map((r) => (
          <ToggleGroupItem
            key={r}
            value={r}
            className="font-mono text-xs"
            data-testid={`dash-range-${r}`}
          >
            {r}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
    </div>
  )
}

function StateTag({ state }: { state: 'quiet' | 'busy' | 'trouble' }) {
  const map = {
    quiet: { label: 'quiet', cls: 'border-border text-muted-foreground', dot: 'bg-muted-foreground/50' },
    busy: { label: 'live', cls: 'border-emerald-500/40 text-emerald-400', dot: 'bg-emerald-500' },
    trouble: { label: 'attention', cls: 'border-amber-500/40 text-amber-400', dot: 'bg-amber-500' },
  } as const
  const m = map[state]
  return (
    <span
      data-testid={`dash-state-${state}`}
      className={`inline-flex items-center gap-1.5 border px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wider ${m.cls}`}
    >
      <span className={`inline-flex h-1.5 w-1.5 rounded-full ${m.dot}`} />
      {m.label}
    </span>
  )
}

export function FirstRun() {
  return (
    <div className="mx-auto max-w-2xl space-y-6 py-8">
      <div className="space-y-2 text-center">
        <h1 className="text-2xl font-bold">Welcome to MCPlexer</h1>
        <p className="text-sm text-muted-foreground">
          One MCP gateway for your local AI tools. Connect a client, then choose which integrations each workspace can use.
        </p>
      </div>
      <ConfigureWithAI variant="hero" />
      <div className="grid gap-3 sm:grid-cols-2">
        <Link
          to="/harness-setup"
          data-testid="first-run-install"
          className="group flex flex-col gap-1 border border-border bg-card p-4 transition-colors hover:border-primary/40 hover:bg-card/80"
        >
          <div className="flex items-center gap-2">
            <Activity className="h-4 w-4 text-primary" />
            <span className="text-sm font-medium">1. Set up an AI harness</span>
          </div>
          <p className="text-xs text-muted-foreground">
            Claude Code, Cursor, Codex, OpenCode, Gemini, MiMoCode, and Pi - auto-detected where possible.
          </p>
        </Link>
        <Link
          to="/workspaces?view=add-server"
          data-testid="first-run-setup"
          className="group flex flex-col gap-1 border border-border bg-card p-4 transition-colors hover:border-primary/40 hover:bg-card/80"
        >
          <div className="flex items-center gap-2">
            <Server className="h-4 w-4 text-primary" />
            <span className="text-sm font-medium">2. Add an integration</span>
          </div>
          <p className="text-xs text-muted-foreground">
            Pick from the catalog: ClickUp, Linear, Postgres, GitHub, and more.
          </p>
        </Link>
      </div>
    </div>
  )
}

export function LoadingState({ onRetry }: { onRetry: () => void }) {
  return (
    <div className="flex flex-col items-start gap-3">
      <div className="flex items-center gap-2 text-muted-foreground">
        <Activity className="h-3.5 w-3.5 animate-pulse-slow" />
        Loading dashboard...
      </div>
      <RetryButton onRetry={onRetry} testid="dash-loading-retry" tone="muted" />
    </div>
  )
}

export function ErrorState({
  message,
  onRetry,
}: {
  message: string
  onRetry: () => void
}) {
  return (
    <div className="flex flex-col items-start gap-3">
      <p className="font-mono text-sm text-destructive">Error: {message}</p>
      <RetryButton
        onRetry={onRetry}
        testid="dash-error-retry"
        tone="destructive"
      />
    </div>
  )
}

function RetryButton({
  onRetry,
  testid,
  tone,
}: {
  onRetry: () => void
  testid: string
  tone: 'muted' | 'destructive'
}) {
  const cls =
    tone === 'destructive'
      ? 'border-destructive/40 bg-destructive/5 text-destructive hover:bg-destructive/10'
      : 'border-border bg-card/40 text-muted-foreground hover:border-border/80 hover:text-foreground'
  return (
    <button
      type="button"
      onClick={onRetry}
      data-testid={testid}
      className={`border px-2.5 py-1 font-mono text-[11px] uppercase tracking-wider transition-colors ${cls}`}
    >
      retry
    </button>
  )
}
