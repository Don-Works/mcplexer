// incident-row — one incident line in the dashboard IncidentsPanel plus the
// inline action menu (acknowledge / silence / dismiss). Split out of
// incidents-panel.tsx to keep each file under the 300-line ceiling; the panel
// owns the data + optimistic mutation, this file owns the row's rendering.

import { Link } from 'react-router-dom'
import { Bell, BellOff, Check, MoreHorizontal, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel,
  DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { IncidentClassKind, Severity } from '@/api/monitoring'
import { isSuppressed, type DashIncident } from '@/hooks/use-incidents'

export interface SilencePreset {
  label: string
  duration: string
  ms: number
}

const SILENCE_PRESETS: SilencePreset[] = [
  { label: '1 hour', duration: '1h', ms: 3_600_000 },
  { label: '4 hours', duration: '4h', ms: 14_400_000 },
  { label: '24 hours', duration: '24h', ms: 86_400_000 },
]

const SEVERITY_TONE: Record<Severity, 'critical' | 'high' | 'warn' | 'info'> = {
  critical: 'critical', error: 'high', warn: 'warn', info: 'info',
}

const CLASS_KIND_LABEL: Record<IncidentClassKind, string> = {
  absence: 'stopped', collection: 'blind', template: 'log',
  correlation: 'pattern', other: 'incident',
}

export interface IncidentRowActions {
  onAck: (inc: DashIncident) => void
  onSilence: (inc: DashIncident, p: SilencePreset) => void
  onUnsilence: (inc: DashIncident) => void
  onDismiss: (inc: DashIncident) => void
}

// isSilenced reports a silence that is in force RIGHT NOW — the state that
// offers an "unsilence now" affordance rather than fresh silence presets.
// Prefer the daemon's derived flag: it accounts for expiry and for an
// escalation piercing the silence (silence_active=false while silenced_until is
// still future). Fall back to the raw window only for an older daemon.
function isSilenced(inc: DashIncident, now = Date.now()): boolean {
  if (typeof inc.silence_active === 'boolean') return inc.silence_active
  if (!inc.silenced_until) return false
  const until = Date.parse(inc.silenced_until)
  return !Number.isNaN(until) && until > now
}

// isAcked reports an acknowledgement in force right now, preferring the derived
// flag (an escalation pierces an ack) over the raw acked_at timestamp.
function isAcked(inc: DashIncident): boolean {
  if (typeof inc.ack_active === 'boolean') return inc.ack_active
  return Boolean(inc.acked_at)
}

export function IncidentRow({
  inc, busy, ...actions
}: { inc: DashIncident; busy: boolean } & IncidentRowActions) {
  const suppressed = isSuppressed(inc)
  return (
    <li
      className={cn(
        'flex items-start gap-3 px-4 py-2.5 transition-colors hover:bg-accent/10',
        suppressed && 'opacity-60',
      )}
    >
      <Badge tone={SEVERITY_TONE[inc.effective_severity] ?? 'muted'} className="mt-0.5 uppercase">
        {inc.effective_severity}
      </Badge>
      <div className="min-w-0 flex-1">
        <Link
          to={`/monitoring?workspace=${encodeURIComponent(inc.workspace_id)}`}
          className="block truncate text-[13px] font-medium text-foreground hover:underline"
          title={inc.title}
        >
          {inc.title}
        </Link>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
          <OriginTag inc={inc} />
          <Badge tone="mono" className="px-1.5 py-0">
            {CLASS_KIND_LABEL[inc.class_kind] ?? inc.class_kind}
          </Badge>
          <span className="tabular-nums">{formatAge(inc.last_seen)}</span>
          {inc.occurrence_count > 1 && (
            <span className="tabular-nums">×{inc.occurrence_count}</span>
          )}
          {suppressed && (
            <span className="text-amber-400/80" title={suppressionTitle(inc)}>
              {suppressionLabel(inc)}
            </span>
          )}
        </div>
      </div>
      <IncidentActionsMenu inc={inc} busy={busy} {...actions} />
    </li>
  )
}

function IncidentActionsMenu({
  inc, busy, onAck, onSilence, onUnsilence, onDismiss,
}: { inc: DashIncident; busy: boolean } & IncidentRowActions) {
  const silenced = isSilenced(inc)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          className="mt-0.5 shrink-0"
          disabled={busy}
          aria-label={`Actions for ${inc.title}`}
        >
          <MoreHorizontal />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuItem onSelect={() => onAck(inc)}>
          <Check /> Acknowledge
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {silenced ? (
          <DropdownMenuItem onSelect={() => onUnsilence(inc)}>
            <Bell /> Unsilence now
          </DropdownMenuItem>
        ) : (
          <>
            <DropdownMenuLabel className="text-[10px] uppercase tracking-wider text-muted-foreground">
              Silence for
            </DropdownMenuLabel>
            {SILENCE_PRESETS.map((p) => (
              <DropdownMenuItem key={p.duration} onSelect={() => onSilence(inc, p)}>
                <BellOff /> {p.label}
              </DropdownMenuItem>
            ))}
          </>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onSelect={() => onDismiss(inc)}
          className="text-red-400 focus:text-red-300"
        >
          <X /> Dismiss
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function OriginTag({ inc }: { inc: DashIncident }) {
  if (inc.origin.kind === 'peer') {
    return (
      <Badge
        tone="info"
        className="px-1.5 py-0"
        title={`Mirrored from peer ${inc.origin.peerId ?? ''}`}
      >
        {inc.origin.label}
      </Badge>
    )
  }
  return <span className="text-muted-foreground/80">{inc.workspaceName}</span>
}

// --- formatting helpers ---------------------------------------------------

function formatAge(fromISO: string): string {
  const then = Date.parse(fromISO)
  if (Number.isNaN(then)) return ''
  return `${compactDuration(Date.now() - then)} ago`
}

function suppressionLabel(inc: DashIncident): string {
  // Gate the label on the live silence/ack state (derived-flag aware), but read
  // the countdown off silenced_until — the flag says "muted", the timestamp
  // says "for how long".
  if (isSilenced(inc)) {
    if (inc.silenced_until) {
      const remaining = Date.parse(inc.silenced_until) - Date.now()
      if (remaining > 0) return `silenced ${compactDuration(remaining)}`
    }
    return 'silenced'
  }
  if (isAcked(inc)) return inc.acked_by ? `acked by ${inc.acked_by}` : 'acked'
  return ''
}

// suppressionTitle spells out the one nuance a compact chip can't: neither
// silence nor ack is absolute — a severity escalation pierces both and pages.
function suppressionTitle(inc: DashIncident): string {
  if (isSilenced(inc)) {
    return 'Silenced — auto-expires; a severity escalation still pages.'
  }
  if (isAcked(inc)) {
    return 'Acknowledged — quiet unless the severity rises.'
  }
  return ''
}

// compactDuration renders a positive millisecond span as one unit: "just now",
// "43m", "2h", "3d". Deliberately coarse — the panel wants a glance, not seconds.
function compactDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}
