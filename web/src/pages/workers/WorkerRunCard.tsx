// WorkerRunCard — one collapsible card per WorkerRun. The default
// render shows the metrics row (when, status, model, tokens, cost).
// While the run is live, the token + cost counters animate toward the
// latest streamed/polled value via useAnimatedNumber so the row FEELS
// like it's chewing tokens in real time.
//
// Output is teased inline (first 80 chars) before the user has to
// click to expand the full block.

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ChevronDown, ChevronRight, Loader2, ShieldAlert, Square } from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { WorkerRun } from '@/api/workers'
import { cancelWorkerRun } from '@/api/workers'
import { LinkifiedText } from '@/pages/tasks/TaskRef'
import { cn } from '@/lib/utils'
import { relativeTime, statusBadgeClass, summariseModel } from './worker-utils'
import { useWorkerRunStream } from './use-worker-run-stream'
import { useAnimatedNumber } from './use-animated-number'

const OUTPUT_TEASER_CHARS = 80

export function RunCard({
  run: initialRun,
  onCancelled,
}: {
  run: WorkerRun
  onCancelled?: () => void
}) {
  const isLiveInitial = initialRun.status === 'running'
  // Subscribe to SSE — backend publishes status + usage + text_delta +
  // tool_call events live off the runner's RunBus, no client polling
  // needed.
  const { run: streamed, liveTranscript } = useWorkerRunStream(
    initialRun.worker_id,
    initialRun.id,
    { enabled: isLiveInitial },
  )
  const run = streamed ?? initialRun
  const [open, setOpen] = useState(false)
  const [cancelling, setCancelling] = useState(false)
  const finished = run.finished_at ? new Date(run.finished_at) : null
  const started = new Date(run.started_at)
  const duration = run.duration_ms > 0 ? `${(run.duration_ms / 1000).toFixed(2)}s` : null
  const isLive = run.status === 'running'

  const inTok = useAnimatedNumber(run.input_tokens, 500)
  const outTok = useAnimatedNumber(run.output_tokens, 500)
  const cost = useAnimatedNumber(run.cost_usd, 500)

  async function handleHardStop() {
    if (!isLive || cancelling) return
    const ok = window.confirm(
      'Hard stop this run? This immediately cancels the model subprocess and marks the run as cancelled. It cannot be undone.',
    )
    if (!ok) return
    setCancelling(true)
    try {
      await cancelWorkerRun(run.id)
      toast.success('Hard stop requested — run will terminate shortly')
      onCancelled?.()
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e || 'cancel failed')
      if (msg.includes('409') || msg.includes('already finished') || msg.includes('not cancellable')) {
        toast.info('Run already finished; not cancellable')
      } else if (msg.includes('404') || msg.includes('not found')) {
        toast.error('Run not found')
      } else {
        toast.error(`Failed to cancel run: ${msg}`)
      }
    } finally {
      setCancelling(false)
    }
  }

  return (
    <div
      className="rounded-md border border-border bg-card/40 p-3 text-sm"
      data-testid={`worker-run-card-${run.id}`}
    >
      <div className="flex flex-wrap items-center gap-3">
        <Badge variant="outline" className={statusBadgeClass(run.status)}>
          {isLive ? <Loader2 className="mr-1 h-3 w-3 animate-spin" /> : null}
          {run.status}
        </Badge>
        <span className="text-xs text-muted-foreground">{relativeTime(run.started_at)}</span>
        {duration && <span className="text-xs text-muted-foreground">· {duration}</span>}
        {isLive && (
          <Button
            size="sm"
            variant="destructive"
            className="h-6 px-2 text-[10px]"
            disabled={cancelling}
            onClick={handleHardStop}
            title="Hard stop: immediately cancel the live model run and kill any subprocess"
            data-testid={`worker-run-hard-stop-${run.id}`}
          >
            {cancelling ? <Loader2 className="mr-1 h-3 w-3 animate-spin" /> : <Square className="mr-1 h-3 w-3" />}
            {cancelling ? 'Stopping…' : 'Hard stop'}
          </Button>
        )}
        <span className="ml-auto truncate font-mono text-[10px] text-muted-foreground/70" title={run.id}>{run.id}</span>
      </div>

      <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-4">
        <MetricRow label="Model" value={summariseModel(run.model_provider, run.model_id)} />
        <MetricRow
          label="Tokens"
          value={`${Math.round(inTok).toLocaleString()} in / ${Math.round(outTok).toLocaleString()} out`}
          live={isLive}
        />
        <MetricRow label="Cost" value={`$${cost.toFixed(4)}`} live={isLive} />
        <MetricRow
          label="Tool calls"
          value={String(run.tool_calls_count)}
          live={isLive}
          hint={
            run.tool_calls_count_source === 'derived'
              ? 'Derived from audit_records — CLI adapters (claude_cli / opencode_cli / grok_cli / mimo_cli) dispatch tools via their own MCP connection. max_tool_calls is enforced via cli_audit counting at finalize, not the gateway loop.'
              : undefined
          }
        />
      </div>

      {run.tool_calls_cap_exceeded && (
        <div className="mt-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-2.5 py-1.5 text-[11px] text-amber-200">
          Tool-call cap exceeded
          {run.tool_calls_cap_scope === 'cli_audit' ? ' (CLI audit count)' : ''}
        </div>
      )}

      {run.deliverable_status && run.deliverable_status !== 'unknown' && (
        <div
          className={cn(
            'mt-2 rounded-md border px-2.5 py-1.5 text-[11px]',
            run.deliverable_status === 'success_with_output'
              ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-200'
              : run.deliverable_status === 'spend_no_commit'
                ? 'border-amber-500/30 bg-amber-500/10 text-amber-200'
                : 'border-red-500/30 bg-red-500/10 text-red-200',
          )}
        >
          Deliverable: {run.deliverable_status.replaceAll('_', ' ')}
          {run.deliverable_branch ? ` · branch ${run.deliverable_branch}` : ''}
          {run.deliverable_commit ? ` · ${run.deliverable_commit.slice(0, 12)}` : ''}
        </div>
      )}

      {run.status === 'awaiting_approval' && (
        <div className="mt-3 flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-2.5 text-xs text-amber-300">
          <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
          <div>
            This run is paused awaiting your approval — see the{' '}
            <Link
              to={`/worker-approvals?run_id=${encodeURIComponent(run.id)}`}
              className="underline underline-offset-2 hover:text-amber-200"
            >
              Approvals panel
            </Link>
            .
          </div>
        </div>
      )}

      {run.error && (
        <div className="mt-3 rounded-md border border-destructive/30 bg-destructive/5 p-2.5 text-xs text-destructive whitespace-pre-wrap">
          {run.error}
        </div>
      )}

      <OutputBlock
        open={open}
        setOpen={setOpen}
        output={isLive && !run.output_text ? liveTranscript : run.output_text}
        live={isLive && !run.output_text}
      />
      {finished && (
        <div className="mt-2 text-[10px] text-muted-foreground/60">
          started {started.toLocaleString()} · finished {finished.toLocaleString()}
        </div>
      )}
    </div>
  )
}

function OutputBlock({
  open,
  setOpen,
  output,
  live,
}: {
  open: boolean
  setOpen: (v: boolean) => void
  output: string
  live?: boolean
}) {
  const teaser = output.slice(0, OUTPUT_TEASER_CHARS).replace(/\s+/g, ' ').trim()
  const bytes = output.length
  const kb = (bytes / 1024).toFixed(1)
  const hasMore = bytes > OUTPUT_TEASER_CHARS
  const label = live ? 'live output' : 'output'
  return (
    <div className="mt-2">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        {open
          ? `Hide ${label}`
          : hasMore
            ? `Show ${label} (${bytes < 1024 ? `${bytes}b` : `${kb}kb`})`
            : `Show ${label}`}
      </button>
      {!open && teaser && (
        <span className="ml-2 truncate text-[11px] text-muted-foreground/70">{teaser}{hasMore ? '…' : ''}</span>
      )}
      {open && (
        <div className={`mt-2 max-h-72 overflow-auto border p-3 font-mono text-[11px] whitespace-pre-wrap ${live ? 'border-sky-500/30 bg-sky-500/5' : 'border-border/60 bg-background/60'}`}>
          {output ? (
            <LinkifiedText text={output} />
          ) : (
            <span className="text-muted-foreground/70">{live ? '(waiting for first token…)' : '(no output captured)'}</span>
          )}
        </div>
      )}
    </div>
  )
}

function MetricRow({
  label,
  value,
  live,
  hint,
}: {
  label: string
  value: string
  live?: boolean
  // Optional hover-tooltip text. When present, a small "?" glyph is
  // rendered next to the label so operators can hover for context —
  // used to flag tool_calls_count_source === 'derived' on CLI runs.
  hint?: string
}) {
  return (
    <div>
      <div className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-muted-foreground/60">
        <span>{label}</span>
        {hint && (
          <span
            className="cursor-help font-mono text-[9px] text-muted-foreground/70 underline decoration-dotted"
            title={hint}
            aria-label={hint}
          >
            ?
          </span>
        )}
      </div>
      <div className={`font-mono text-xs ${live ? 'text-sky-300' : 'text-foreground'}`}>{value}</div>
    </div>
  )
}
