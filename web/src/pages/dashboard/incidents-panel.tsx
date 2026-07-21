// IncidentsPanel — "what is currently wrong, anywhere" on the dashboard.
//
// Consumes the cross-workspace incident feed (useDashboardIncidents) and lets
// the operator act on each one inline: acknowledge (seen, stop nagging),
// silence for a bounded window, or dismiss (it's over / not worth tracking).
// Mutations are optimistic with rollback on failure, and the row shows the
// suppression state the operator has already applied.
//
// Layout follows AttentionCard: a bordered section with a count in the header
// and a divided list. When everything is clear it collapses to a single calm
// line so it never dominates a quiet dashboard.

import { useState } from 'react'
import { toast } from 'sonner'
import { AlertTriangle, CheckCircle2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  ackIncident, dismissIncident, silenceIncident, unsilenceIncident,
} from '@/api/monitoring'
import {
  useDashboardIncidents, isSuppressed, type DashIncident,
} from '@/hooks/use-incidents'
import {
  IncidentRow, type IncidentRowActions, type SilencePreset,
} from './incident-row'

export function IncidentsPanel() {
  const { incidents, loading, error, refetch, mutate } = useDashboardIncidents()
  const [busyId, setBusyId] = useState<string | null>(null)

  async function runAction(
    inc: DashIncident,
    optimistic: (prev: DashIncident[]) => DashIncident[],
    call: () => Promise<unknown>,
    okMessage: string,
  ) {
    setBusyId(inc.id)
    const rollback = mutate(optimistic)
    try {
      await call()
      toast.success(okMessage)
      refetch()
    } catch (e) {
      rollback()
      toast.error(String(e))
    } finally {
      setBusyId(null)
    }
  }

  const actions: IncidentRowActions = {
    onAck: (inc) =>
      runAction(
        inc,
        // Set the derived flags too, not just the raw column: the row reads
        // suppression off suppressed/ack_active now. refetch() replaces this
        // guess with the daemon's recomputed flags on success.
        patchOne(inc.id, {
          suppressed: true, ack_active: true, acked_at: new Date().toISOString(),
        }),
        () => ackIncident(inc.workspace_id, inc.id),
        `acknowledged · ${inc.title}`,
      ),
    onSilence: (inc, p: SilencePreset) =>
      runAction(
        inc,
        patchOne(inc.id, {
          suppressed: true,
          silence_active: true,
          silenced_until: new Date(Date.now() + p.ms).toISOString(),
        }),
        () => silenceIncident(inc.workspace_id, inc.id, p.duration),
        `silenced ${p.label} · ${inc.title}`,
      ),
    onUnsilence: (inc) =>
      runAction(
        inc,
        // Lifting the silence only un-suppresses if no ack is still in force.
        patchOne(inc.id, {
          silence_active: false,
          silenced_until: undefined,
          suppressed: Boolean(inc.ack_active ?? inc.acked_at),
        }),
        () => unsilenceIncident(inc.workspace_id, inc.id),
        `unsilenced · ${inc.title}`,
      ),
    onDismiss: (inc) =>
      runAction(
        inc,
        (prev) => prev.filter((i) => i.id !== inc.id),
        () => dismissIncident(inc.workspace_id, inc.id),
        `dismissed · ${inc.title}`,
      ),
  }

  const criticalCount = incidents.filter(
    (i) => i.effective_severity === 'critical' && !isSuppressed(i),
  ).length

  return (
    <section data-testid="dash-incidents" className="border border-border bg-card/40">
      <header className="flex items-center justify-between border-b border-border/60 px-4 py-2">
        <h2 className="text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          Incidents
        </h2>
        <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
          {incidents.length === 0
            ? 'clear'
            : criticalCount > 0
              ? `${criticalCount} critical · ${incidents.length}`
              : incidents.length}
        </span>
      </header>
      <IncidentsBody
        incidents={incidents}
        loading={loading}
        error={error}
        busyId={busyId}
        actions={actions}
      />
    </section>
  )
}

function IncidentsBody({
  incidents, loading, error, busyId, actions,
}: {
  incidents: DashIncident[]
  loading: boolean
  error: string | null
  busyId: string | null
  actions: IncidentRowActions
}) {
  if (loading && incidents.length === 0) {
    return <PanelLine>Checking every workspace for open incidents…</PanelLine>
  }
  if (error && incidents.length === 0) {
    return (
      <PanelLine tone="warn">
        <AlertTriangle className="h-3.5 w-3.5" /> Could not load incidents — {error}
      </PanelLine>
    )
  }
  if (incidents.length === 0) {
    return (
      <PanelLine tone="ok">
        <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500/70" /> All clear across every
        workspace. Nothing is on fire.
      </PanelLine>
    )
  }
  return (
    <ul className="divide-y divide-border/40">
      {incidents.map((inc) => (
        <IncidentRow key={inc.id} inc={inc} busy={busyId === inc.id} {...actions} />
      ))}
    </ul>
  )
}

function PanelLine({
  children, tone = 'muted',
}: { children: React.ReactNode; tone?: 'muted' | 'ok' | 'warn' }) {
  return (
    <div
      className={cn(
        'flex items-center gap-2 px-4 py-3 text-[13px]',
        tone === 'warn' ? 'text-amber-300' : 'text-muted-foreground',
      )}
    >
      {children}
    </div>
  )
}

function patchOne(id: string, patch: Partial<DashIncident>) {
  return (prev: DashIncident[]) =>
    prev.map((i) => (i.id === id ? { ...i, ...patch } : i))
}
