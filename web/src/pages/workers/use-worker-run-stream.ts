// use-worker-run-stream — subscribe to the workers run SSE endpoint.
// Backend now publishes four event names off the runner's RunBus:
//
//   - status     — full WorkerRun JSON, initial snapshot + every status
//                  transition + terminal row. Identical wire format to
//                  the v1 endpoint so any other consumer keeps working.
//   - text_delta — one adapter turn's assistant prose. We accumulate
//                  these into liveTranscript so the UI can show the
//                  worker "talking" mid-run instead of waiting for
//                  finalize.
//   - tool_call  — one in-flight tool dispatch. Pushed into liveToolCalls
//                  so the live tail can show "→ called search_repos".
//   - usage      — cumulative tokens + cost after each turn. Overlaid
//                  onto the run row so token counters tick smoothly
//                  without a separate poll.
//
// We close the EventSource as soon as the row reaches a terminal status —
// EventSource auto-reconnects by default, and we don't want a tight
// retry loop chasing a finished run.

import { useEffect, useRef, useState } from 'react'

import type { WorkerRun, WorkerRunStatus } from '@/api/workers'

const TERMINAL: ReadonlySet<WorkerRunStatus> = new Set([
  'success',
  'failure',
  'cap_exceeded',
  'rejected',
  'awaiting_approval',
  'cancelled',
])

interface Options {
  // enabled=false disables the subscription entirely. Used to skip
  // streaming for already-terminal runs from the parent.
  enabled: boolean
}

// LiveToolCall mirrors the backend RunEvent shape for kind=tool_call so
// consumers don't have to re-derive iteration / allowed.
export interface LiveToolCall {
  iteration: number
  name: string
  inputJSON: string
  allowed: boolean
}

// WorkerRunStream is the bundle of state the hook exposes. `run` is the
// last-known WorkerRun (snapshot OR usage-overlay). `liveTranscript` is
// the in-memory accumulation of text_delta events — empty until the
// model emits prose. `liveToolCalls` is the in-order log of tool_call
// events. Both live arrays/strings reset when run_id changes.
export interface WorkerRunStream {
  run: WorkerRun | null
  liveTranscript: string
  liveToolCalls: LiveToolCall[]
}

interface RunEventEnvelope {
  kind: string
  worker_id?: string
  run_id?: string
  iteration?: number
  text?: string
  tool_name?: string
  tool_input_json?: string
  tool_allowed?: boolean
  input_tokens?: number
  output_tokens?: number
  cost_usd?: number
  tool_calls?: number
}

export function useWorkerRunStream(
  workerID: string,
  runID: string,
  opts: Options,
): WorkerRunStream {
  const [state, setState] = useState<WorkerRunStream>({
    run: null,
    liveTranscript: '',
    liveToolCalls: [],
  })
  const sourceRef = useRef<EventSource | null>(null)

  useEffect(() => {
    // Reset the live buffers whenever the run we're tailing changes —
    // a stale transcript from the previous run must not leak into the
    // new one.
    setState({ run: null, liveTranscript: '', liveToolCalls: [] })

    if (!opts.enabled || !workerID || !runID) return
    const apiBase =
      import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
    const url = `${apiBase}/api/v1/workers/${encodeURIComponent(
      workerID,
    )}/runs/${encodeURIComponent(runID)}/events`
    const es = new EventSource(url, { withCredentials: true })
    sourceRef.current = es

    es.addEventListener('status', (e) => {
      try {
        const next = JSON.parse((e as MessageEvent).data) as WorkerRun
        setState((prev) => ({ ...prev, run: next }))
        if (TERMINAL.has(next.status)) {
          es.close()
          sourceRef.current = null
        }
      } catch {
        // Swallow — partial frame, next event will retry.
      }
    })

    es.addEventListener('text_delta', (e) => {
      try {
        const ev = JSON.parse((e as MessageEvent).data) as RunEventEnvelope
        if (!ev.text) return
        setState((prev) => ({
          ...prev,
          liveTranscript: prev.liveTranscript
            ? prev.liveTranscript + '\n\n' + ev.text
            : (ev.text ?? ''),
        }))
      } catch {
        /* swallow */
      }
    })

    es.addEventListener('tool_call', (e) => {
      try {
        const ev = JSON.parse((e as MessageEvent).data) as RunEventEnvelope
        if (!ev.tool_name) return
        setState((prev) => ({
          ...prev,
          liveToolCalls: [
            ...prev.liveToolCalls,
            {
              iteration: ev.iteration ?? 0,
              name: ev.tool_name ?? '',
              inputJSON: ev.tool_input_json ?? '',
              allowed: ev.tool_allowed ?? true,
            },
          ],
        }))
      } catch {
        /* swallow */
      }
    })

    es.addEventListener('usage', (e) => {
      try {
        const ev = JSON.parse((e as MessageEvent).data) as RunEventEnvelope
        // Overlay cumulative usage on the most recent run snapshot so
        // token / cost counters tick smoothly between status frames.
        setState((prev) => {
          if (!prev.run) return prev
          return {
            ...prev,
            run: {
              ...prev.run,
              input_tokens: ev.input_tokens ?? prev.run.input_tokens,
              output_tokens: ev.output_tokens ?? prev.run.output_tokens,
              cost_usd: ev.cost_usd ?? prev.run.cost_usd,
              tool_calls_count: ev.tool_calls ?? prev.run.tool_calls_count,
            },
          }
        })
      } catch {
        /* swallow */
      }
    })

    es.onerror = () => {
      // EventSource auto-retries; closing here would cause a tight loop.
    }
    return () => {
      es.close()
      sourceRef.current = null
    }
  }, [workerID, runID, opts.enabled])

  return state
}
