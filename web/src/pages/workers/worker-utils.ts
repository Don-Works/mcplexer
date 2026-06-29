// Small presentation helpers shared by every page in the Workers
// section. Anything that touches a Worker / WorkerRun and renders a
// label, badge class, or human-readable string belongs here so the
// pages stay focused on layout.

import type { WorkerRunStatus, WorkerSummary } from '@/api/workers'

// statusBadgeClass returns the Tailwind class for a run-status badge.
// Mirrors the color story used by the Guards pages: green for success,
// red for failure, amber for awaiting_approval, orange for cap_exceeded
// (deliberately distinct so the two warning states don't collide),
// sky for running (with pulse), gray for unknown / never-run / paused.
//
// All badges use 2-stop gradients so they look "alive" instead of flat,
// and the live states (running, awaiting_approval) carry their own
// pulse rates picked to convey appropriate urgency.
// Flat colored pills. Hue conveys severity; motion is reserved for the
// row-level "this is happening right now" signal, not the badge itself.
export function statusBadgeClass(status?: WorkerRunStatus | string | ''): string {
  switch (status) {
    case 'success':
      return 'bg-emerald-500/10 text-emerald-300 border-emerald-500/40'
    case 'failure':
    case 'rejected':
      return 'bg-destructive/10 text-destructive border-destructive/40'
    case 'partial':
      return 'bg-amber-500/10 text-amber-300 border-amber-500/40'
    case 'awaiting_approval':
      return 'bg-amber-500/10 text-amber-300 border-amber-500/40'
    case 'cap_exceeded':
      return 'bg-orange-500/10 text-orange-300 border-orange-500/40'
    case 'cancelled':
      // Operator hard-stop: slate, deliberately distinct from failure
      // red — a cancelled run is an intentional stop, not an error.
      return 'bg-slate-500/10 text-slate-300 border-slate-500/40'
    case 'blocked':
      // Gated by a pre/post-execute hook: violet, distinct from failure
      // red and cancel slate — an intentional policy stop, not an error.
      return 'bg-violet-500/10 text-violet-300 border-violet-500/40'
    case 'running':
      return 'bg-sky-500/10 text-sky-300 border-sky-500/40'
    case 'paused':
      return 'bg-muted/50 text-muted-foreground border-border'
    default:
      return 'bg-muted/30 text-muted-foreground border-border'
  }
}

// relativeTime renders an ISO timestamp as "5m ago" / "3h ago" / etc.
// Returns "just now" for sub-minute deltas and the date for >7 days.
export function relativeTime(iso?: string): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const diff = Date.now() - t
  if (diff < 0) return 'in the future'
  const s = Math.floor(diff / 1000)
  if (s < 60) return s <= 5 ? 'just now' : `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 7) return `${d}d ago`
  return new Date(iso).toLocaleDateString()
}

// humanizeSchedule does best-effort cron → English. Returns null when
// the spec is unparseable so callers can decide between showing the
// English form and the raw spec. The string-returning variant
// (humanizeScheduleOrRaw) falls back to the raw spec.
export function humanizeSchedule(spec: string): string {
  const parsed = humanizeScheduleOrNull(spec)
  return parsed ?? (spec || '—')
}

export function humanizeScheduleOrNull(spec: string): string | null {
  if (!spec) return null
  const trimmed = spec.trim()
  if (isTriggerOnlySchedule(trimmed)) return TRIGGER_ONLY_LABEL
  if (matchIntervalSpec(trimmed)) return `every ${trimmed}`
  return humanizeCron(trimmed)
}

// Schedules with an interval >= 30 days are placeholders for "fires on
// demand" workers — manual run or mesh trigger. Showing "next in 364d 23h"
// for an 8760h spec is noise; the operator wants the affordance to be
// obvious, not a misleading countdown.
export const TRIGGER_ONLY_THRESHOLD_MS = 30 * 24 * 60 * 60 * 1_000
export const TRIGGER_ONLY_LABEL = 'manual / mesh trigger'

export function isTriggerOnlySchedule(spec: string | undefined | null): boolean {
  if (!spec) return false
  const ms = parseIntervalMs(spec.trim())
  return ms !== null && ms >= TRIGGER_ONLY_THRESHOLD_MS
}

function matchIntervalSpec(spec: string): boolean {
  // Go duration: e.g. "5m", "1h30m", "2h30m15s", "750ms" (ms is rare).
  return /^(\d+(ns|us|µs|ms|s|m|h))+$/.test(spec)
}

function humanizeCron(spec: string): string | null {
  const parts = spec.split(/\s+/)
  if (parts.length !== 5) return null
  const [m, h, dom, mon, dow] = parts
  if (m === '*' && h === '*' && dom === '*' && mon === '*' && dow === '*') {
    return 'every minute'
  }
  if (m === '0' && h === '*' && dom === '*' && mon === '*' && dow === '*') {
    return 'every hour'
  }
  if (/^\d+$/.test(m) && /^\d+$/.test(h) && dom === '*' && mon === '*' && dow === '*') {
    return `daily at ${h.padStart(2, '0')}:${m.padStart(2, '0')}`
  }
  if (/^\d+$/.test(m) && /^\d+$/.test(h) && dom === '*' && mon === '*' && dow === '1-5') {
    return `weekdays at ${h.padStart(2, '0')}:${m.padStart(2, '0')}`
  }
  if (m.startsWith('*/') && h === '*' && dom === '*' && mon === '*' && dow === '*') {
    return `every ${m.slice(2)} minutes`
  }
  if (m === '0' && h.startsWith('*/') && dom === '*' && mon === '*' && dow === '*') {
    return `every ${h.slice(2)} hours`
  }
  return null
}

// summariseModel turns "anthropic" + "claude-opus-4-7" → "anthropic /
// claude-opus-4-7" for compact list-page cells.
export function summariseModel(provider: string, modelID: string): string {
  if (!provider && !modelID) return '—'
  if (!provider) return modelID
  if (!modelID) return provider
  return `${provider} / ${modelID}`
}

// runningCount sums the workers that are currently running. Used by
// the sidebar live-badge.
export function runningCount(rows: WorkerSummary[] | null | undefined): number {
  if (!rows) return 0
  let n = 0
  for (const r of rows) {
    if (r.last_run_status === 'running') n++
  }
  return n
}

// runningWorkers returns the subset of WorkerSummary currently in
// "running" status — used by the AppLayout's persistent "Now Running"
// strip.
export function runningWorkers(rows: WorkerSummary[] | null | undefined): WorkerSummary[] {
  if (!rows) return []
  return rows.filter((r) => r.last_run_status === 'running')
}

export function isDelegationWorker(row: WorkerSummary): boolean {
  return Boolean(row.ephemeral || row.delegation_id)
}

export function isDurableWorker(row: WorkerSummary): boolean {
  return !isDelegationWorker(row)
}

export function durableWorkers(rows: WorkerSummary[] | null | undefined): WorkerSummary[] {
  if (!rows) return []
  return rows.filter(isDurableWorker)
}

export function liveDurableWorkers(rows: WorkerSummary[] | null | undefined): WorkerSummary[] {
  return durableWorkers(rows).filter((row) => row.last_run_status === 'running')
}

export function isLiveDelegationWorker(row: WorkerSummary): boolean {
  if (!isDelegationWorker(row)) return false
  return row.last_run_status === 'running' || row.last_run_status === 'awaiting_approval'
}

export function liveDelegationWorkers(rows: WorkerSummary[] | null | undefined): WorkerSummary[] {
  if (!rows) return []
  return rows.filter(isLiveDelegationWorker)
}

export function liveDelegationCount(rows: WorkerSummary[] | null | undefined): number {
  const ids = new Set<string>()
  for (const row of liveDelegationWorkers(rows)) {
    ids.add(row.delegation_id || row.id)
  }
  return ids.size
}

// shortID renders a long worker / run ID as the last 8 characters with
// an em-dash prefix so users still see "this is shorthand". Used in
// vitals strips and headers where the full ID is overkill.
export function shortID(id: string): string {
  if (!id) return ''
  if (id.length <= 12) return id
  return '…' + id.slice(-8)
}

// nextRunCountdown computes when a Worker will next fire and returns
// both a human countdown and the absolute Date. Best-effort cron+
// interval parser: anything we can't read returns nextRunDate=null and
// the consumer falls back to the raw spec.
//
// For intervals we anchor off lastRunAt+interval; if there's no
// lastRunAt we treat "now" as t=0 and fire at now+interval (a sensible
// default for an enabled-but-never-run worker).
//
// Workers with a schedule_spec interval >= TRIGGER_ONLY_THRESHOLD_MS surface
// TRIGGER_ONLY_LABEL ("manual / mesh trigger") instead of a misleading "next
// run in 364d" countdown.
export interface CountdownResult {
  humanCountdown: string
  nextRunDate: Date | null
}

export function nextRunCountdown(
  scheduleSpec: string,
  lastRunAt: string | undefined,
  now: Date = new Date(),
): CountdownResult {
  if (!scheduleSpec) return { humanCountdown: '—', nextRunDate: null }
  const trimmed = scheduleSpec.trim()
  const intervalMs = parseIntervalMs(trimmed)
  if (intervalMs !== null) {
    if (intervalMs >= TRIGGER_ONLY_THRESHOLD_MS) {
      return { humanCountdown: TRIGGER_ONLY_LABEL, nextRunDate: null }
    }
    const anchor = lastRunAt ? new Date(lastRunAt).getTime() : now.getTime()
    let next = anchor + intervalMs
    while (next <= now.getTime()) next += intervalMs
    return { humanCountdown: formatCountdown(next - now.getTime()), nextRunDate: new Date(next) }
  }
  const cronNext = nextCronFire(trimmed, now)
  if (cronNext) {
    return { humanCountdown: formatCountdown(cronNext.getTime() - now.getTime()), nextRunDate: cronNext }
  }
  return { humanCountdown: '—', nextRunDate: null }
}

// formatCountdown turns ms-until → "23m 14s" / "3h 12m" / "<1m".
export function formatCountdown(ms: number): string {
  if (ms <= 0) return 'now'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const remS = s % 60
  if (m < 60) return remS > 0 ? `${m}m ${remS.toString().padStart(2, '0')}s` : `${m}m`
  const h = Math.floor(m / 60)
  const remM = m % 60
  if (h < 24) return `${h}h ${remM.toString().padStart(2, '0')}m`
  const d = Math.floor(h / 24)
  return `${d}d ${h % 24}h`
}

// parseIntervalMs reads Go duration like "5m" or "1h30m" into ms.
// Returns null when it doesn't look like an interval spec.
function parseIntervalMs(spec: string): number | null {
  if (!/^(\d+(ns|us|µs|ms|s|m|h))+$/.test(spec)) return null
  const re = /(\d+)(ns|us|µs|ms|s|m|h)/g
  let total = 0
  let match: RegExpExecArray | null
  while ((match = re.exec(spec)) !== null) {
    const n = Number(match[1])
    switch (match[2]) {
      case 'ns':
        total += n / 1_000_000
        break
      case 'us':
      case 'µs':
        total += n / 1_000
        break
      case 'ms':
        total += n
        break
      case 's':
        total += n * 1_000
        break
      case 'm':
        total += n * 60_000
        break
      case 'h':
        total += n * 3_600_000
        break
    }
  }
  return total > 0 ? total : null
}

// nextCronFire — five-field cron resolver. Supports the same dialect
// as humanizeCron plus a brute-force scan that handles arbitrary
// minute/hour/dom/dow patterns. The scan walks one minute at a time
// from `now+1m` up to two years ahead; cheap enough at our cadence and
// avoids importing a full cron lib for M0.
function nextCronFire(spec: string, now: Date): Date | null {
  const parts = spec.split(/\s+/)
  if (parts.length !== 5) return null
  const fields = parts.map(parseField)
  if (fields.some((f) => f === null)) return null
  const start = new Date(now.getTime() + 60_000 - (now.getTime() % 60_000))
  const maxIter = 60 * 24 * 366 * 2
  const cur = new Date(start)
  for (let i = 0; i < maxIter; i++) {
    if (matchesCron(cur, fields as CronField[])) return cur
    cur.setMinutes(cur.getMinutes() + 1)
  }
  return null
}

interface CronField {
  values: Set<number>
}

function parseField(raw: string): CronField | null {
  // Field forms: *, N, A-B, */N, A,B,C. No ranges-with-steps for M0.
  const parts = raw.split(',')
  const values = new Set<number>()
  for (const p of parts) {
    if (p === '*') return { values: new Set([-1]) } // sentinel "any"
    const step = p.match(/^\*\/(\d+)$/)
    if (step) {
      const n = Number(step[1])
      if (n <= 0) return null
      for (let i = 0; i < 60; i += n) values.add(i)
      continue
    }
    const range = p.match(/^(\d+)-(\d+)$/)
    if (range) {
      const a = Number(range[1])
      const b = Number(range[2])
      for (let i = a; i <= b; i++) values.add(i)
      continue
    }
    if (/^\d+$/.test(p)) {
      values.add(Number(p))
      continue
    }
    return null
  }
  return { values }
}

function matchesCron(d: Date, fields: CronField[]): boolean {
  const [m, h, dom, mon, dow] = fields
  return (
    fieldMatches(m, d.getMinutes()) &&
    fieldMatches(h, d.getHours()) &&
    fieldMatches(dom, d.getDate()) &&
    fieldMatches(mon, d.getMonth() + 1) &&
    fieldMatches(dow, d.getDay())
  )
}

function fieldMatches(f: CronField, v: number): boolean {
  if (f.values.has(-1)) return true
  return f.values.has(v)
}

// startOfLocalDay returns midnight of the calling user's local day.
export function startOfLocalDay(d: Date = new Date()): Date {
  const out = new Date(d)
  out.setHours(0, 0, 0, 0)
  return out
}
