// WorkerLiveTail — visible only while at least one run is in flight.
// Renders a compact "live" strip directly under the vitals row: status
// pip, elapsed-time clock counting up every second, animated token /
// cost counters, plus a live transcript window showing the assistant's
// prose and any tool calls as they happen.
//
// Wires straight to useWorkerRunStream — backend now publishes
// text_delta + tool_call + usage events off the runner's RunBus, so
// there's no client polling anywhere on this surface.

import { useEffect, useState } from 'react'
import { Activity, Wrench } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import type { WorkerRun, WorkerRunStatus } from '@/api/workers'
import { formatCountdown, statusBadgeClass } from './worker-utils'
import { useWorkerRunStream } from './use-worker-run-stream'
import { useAnimatedNumber } from './use-animated-number'

interface Props {
  liveRun: WorkerRun
  onRunSettled?: () => void
}

const TERMINAL: ReadonlySet<WorkerRunStatus> = new Set([
  'success',
  'failure',
  'cap_exceeded',
  'paused',
  'rejected',
  'awaiting_approval',
  'blocked',
])

export function WorkerLiveTail({ liveRun, onRunSettled }: Props) {
  const { run: streamed, liveTranscript, liveToolCalls } = useWorkerRunStream(
    liveRun.worker_id,
    liveRun.id,
    { enabled: liveRun.status === 'running' },
  )
  const run = streamed ?? liveRun
  const elapsed = useElapsed(new Date(run.started_at).getTime())
  const inTokens = useAnimatedNumber(run.input_tokens, 600)
  const outTokens = useAnimatedNumber(run.output_tokens, 600)
  const cost = useAnimatedNumber(run.cost_usd, 600)

  useEffect(() => {
    if (!streamed || !TERMINAL.has(streamed.status)) return
    onRunSettled?.()
  }, [onRunSettled, streamed?.id, streamed?.status])

  return (
    <div
      className="flex flex-col gap-2 border border-sky-500/30 bg-sky-500/5 p-3 text-sm"
      data-testid="worker-live-tail"
    >
      <div className="flex flex-wrap items-center gap-4">
        <Badge variant="outline" className={statusBadgeClass(run.status)}>
          <Activity className="mr-1 h-3 w-3" />
          live
        </Badge>
        <div className="flex items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
            elapsed
          </span>
          <span className="font-mono text-sm font-medium text-sky-300">
            {formatCountdown(elapsed)}
          </span>
        </div>
        <div className="flex items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
            tokens
          </span>
          <span className="font-mono text-sm text-foreground">
            {Math.round(inTokens).toLocaleString()} in / {Math.round(outTokens).toLocaleString()} out
          </span>
        </div>
        <div className="flex items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
            cost
          </span>
          <span className="font-mono text-sm text-foreground">${cost.toFixed(4)}</span>
        </div>
        <div className="flex items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">
            tool calls
          </span>
          <span className="font-mono text-sm text-foreground">{run.tool_calls_count}</span>
          {run.tool_calls_count_source === 'derived' && (
            <span
              className="cursor-help font-mono text-[9px] text-muted-foreground/70 underline decoration-dotted"
              title="Derived from audit_records — adapter family (claude_cli / opencode_cli / grok_cli / mimo_cli) does not yet report tool_use events natively. Real tool calls happen out-of-band via the child CLI's own MCP connection back to the gateway."
              aria-label="tool_calls_count derived from audit_records"
            >
              ?
            </span>
          )}
        </div>
        <div className="ml-auto truncate font-mono text-[10px] text-muted-foreground/70">
          run {run.id}
        </div>
      </div>

      {(liveTranscript || liveToolCalls.length > 0) && (
        <div className="grid gap-2 md:grid-cols-2">
          {liveTranscript && (
            <div className="flex max-h-40 flex-col overflow-hidden border border-border/40 bg-background/40">
              <div className="border-b border-border/40 bg-muted/30 px-2 py-1 text-[10px] uppercase tracking-wider text-muted-foreground/70">
                assistant
              </div>
              <pre className="flex-1 overflow-auto whitespace-pre-wrap break-words p-2 text-xs leading-relaxed text-foreground/90">
                {liveTranscript}
              </pre>
            </div>
          )}
          {liveToolCalls.length > 0 && (
            <div className="flex max-h-40 flex-col overflow-hidden border border-border/40 bg-background/40">
              <div className="border-b border-border/40 bg-muted/30 px-2 py-1 text-[10px] uppercase tracking-wider text-muted-foreground/70">
                tool calls ({liveToolCalls.length})
              </div>
              <ul className="flex-1 overflow-auto p-2 text-xs">
                {liveToolCalls.map((tc, i) => (
                  <li
                    key={`${tc.iteration}-${i}`}
                    className="flex items-start gap-1.5 py-0.5 font-mono"
                  >
                    <Wrench
                      className={`mt-0.5 h-3 w-3 shrink-0 ${
                        tc.allowed ? 'text-sky-400' : 'text-destructive'
                      }`}
                    />
                    <span
                      className={
                        tc.allowed ? 'text-foreground/90' : 'text-destructive line-through'
                      }
                    >
                      {tc.name}
                    </span>
                    {tc.inputJSON && (
                      <span className="truncate text-muted-foreground/70">
                        {tc.inputJSON}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// useElapsed re-renders every second so the elapsed-time clock ticks.
function useElapsed(startedAtMs: number): number {
  const [now, setNow] = useState<number>(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(id)
  }, [])
  return Math.max(0, now - startedAtMs)
}
