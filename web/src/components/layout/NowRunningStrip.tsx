// NowRunningStrip — persistent sticky strip above <main> that surfaces
// every currently-running Worker. Conveys "the gateway is alive" at a
// glance. A subtle horizontal shimmer crosses the strip (animate-shimmer
// in index.css) — the heartbeat.
//
// Each chip is clickable; jumps to the worker's detail page. Up to 3
// chips are visible; the rest collapse behind "+N more".

import { Link } from 'react-router-dom'
import { Activity } from 'lucide-react'

import type { WorkerSummary } from '@/api/workers'

const MAX_VISIBLE = 3

export function NowRunningStrip({ workers }: { workers: WorkerSummary[] }) {
  if (workers.length === 0) return null
  const visible = workers.slice(0, MAX_VISIBLE)
  const extra = workers.length - visible.length

  return (
    <div
      className="relative flex items-center gap-3 overflow-hidden border-b border-sky-500/30 bg-sky-500/5 px-4 py-1.5 text-xs"
      data-testid="now-running-strip"
    >
      <span className="inline-flex items-center gap-1.5 font-medium text-sky-300">
        <span className="h-2 w-2 shrink-0 rounded-full bg-sky-400" />
        Live
      </span>
      <span aria-hidden className="text-muted-foreground/40">·</span>
      <div className="relative flex flex-1 flex-wrap items-center gap-x-3 gap-y-1 overflow-hidden">
        {visible.map((w) => (
          <Link
            key={w.id}
            to={w.ephemeral ? '/delegations' : `/workers/${w.id}`}
            title={`${w.name} · ${summariseModelShort(w.model_provider, w.model_id)}`}
            className="group inline-flex items-center gap-1.5 text-muted-foreground hover:text-foreground"
            data-testid={`now-running-chip-${w.id}`}
          >
            <Activity className="h-3 w-3 text-sky-400" />
            <span className="font-medium text-foreground group-hover:text-sky-300">
              {w.name}
            </span>
            {w.ephemeral && (
              <span className="border border-sky-500/30 px-1 py-0.5 text-[9px] uppercase text-sky-300">
                delegation
              </span>
            )}
            <span className="font-mono text-[10px] text-muted-foreground/60">
              {summariseModelShort(w.model_provider, w.model_id)}
            </span>
          </Link>
        ))}
        {extra > 0 && (
          <Link
            to="/workers"
            title={`${extra} additional running workers`}
            className="text-muted-foreground hover:text-foreground"
          >
            +{extra} more
          </Link>
        )}
      </div>
    </div>
  )
}

// summariseModelShort — last token of the model id (e.g. "claude-4-7"
// instead of "anthropic / claude-opus-4-7") so the strip stays scannable.
function summariseModelShort(provider: string, modelID: string): string {
  if (!modelID) return provider || ''
  return modelID
}
