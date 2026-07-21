// use-incidents — the cross-workspace incident feed behind the dashboard panel.
//
// The read API (GET /monitoring/incidents) is workspace-scoped by design: an
// unscoped list would let one workspace read another's operational state. The
// dashboard wants the opposite — "what is wrong ANYWHERE this daemon can see" —
// so we enumerate the accessible workspaces and union their incidents client
// side. That set already includes workspaces mirrored from monitoring LXCs over
// p2p (a linked/mirror workspace is a local workspace whose link points at a
// peer), so no cross-workspace endpoint is needed; the loop is a handful of
// cheap GETs against an already-open origin.
//
// Each incident is tagged with where it came from (local workspace vs a peer's
// mirror) so an operator can tell one peer's incidents from another's, and
// both from local, at a glance.

import { useCallback, useEffect, useState } from 'react'
import { useInterval } from '@/hooks/use-interval'
import { listWorkspaces, listWorkspaceLinks } from '@/api/client'
import { listIncidents, type MonitoringIncident } from '@/api/monitoring'
import type { Workspace, WorkspaceLink } from '@/api/types'

export interface IncidentOrigin {
  // 'peer' when the workspace is a mirror of a peer's workspace (arrived over
  // p2p task/incident sync); 'local' when it is a first-class local workspace.
  kind: 'local' | 'peer'
  // Display label: the remote workspace/peer name for a mirror, else the local
  // workspace name.
  label: string
  peerId?: string
}

export interface DashIncident extends MonitoringIncident {
  workspaceName: string
  origin: IncidentOrigin
}

interface IncidentsState {
  incidents: DashIncident[]
  loading: boolean
  error: string | null
}

export interface UseIncidentsReturn extends IncidentsState {
  refetch: () => void
  // Optimistically replace the in-memory list. Returns a rollback that
  // restores the exact snapshot captured before the mutation.
  mutate: (fn: (prev: DashIncident[]) => DashIncident[]) => () => void
}

// listWorkspaceLinks throws when the daemon was built without p2p. A missing
// link table just means "nothing is mirrored", not an error the operator
// should see, so we swallow it and fall back to an empty link set.
async function safeLinks(): Promise<WorkspaceLink[]> {
  try {
    return await listWorkspaceLinks()
  } catch {
    return []
  }
}

// buildOriginResolver maps a workspace to its display origin. A workspace whose
// id is the local end of a workspace-link is a mirror of the peer named by the
// link; everything else is local.
function buildOriginResolver(
  links: WorkspaceLink[],
): (ws: Workspace) => IncidentOrigin {
  const byLocalId = new Map<string, WorkspaceLink>()
  for (const link of links) byLocalId.set(link.local_workspace_id, link)
  return (ws: Workspace): IncidentOrigin => {
    const link = byLocalId.get(ws.id)
    if (!link) return { kind: 'local', label: ws.name }
    return {
      kind: 'peer',
      label: link.remote_workspace_name?.trim() || link.peer_id,
      peerId: link.peer_id,
    }
  }
}

const SEVERITY_RANK: Record<string, number> = {
  critical: 0, error: 1, warn: 2, info: 3,
}

// isSuppressed reports whether the routine nag for this incident is currently
// muted (an ack or a silence in force). Suppressed rows sort below live ones so
// unhandled work stays on top. Prefer the daemon's derived `suppressed` flag:
// it folds in escalation-piercing and expiry, so an incident that has escalated
// past the severity it was silenced/acked at correctly reads NOT suppressed
// even while silenced_until is still in the future. Fall back to the raw
// columns only for a daemon predating the derived surface.
export function isSuppressed(inc: MonitoringIncident, now = Date.now()): boolean {
  if (typeof inc.suppressed === 'boolean') return inc.suppressed
  if (inc.acked_at) return true
  if (inc.silenced_until) {
    const until = Date.parse(inc.silenced_until)
    if (!Number.isNaN(until) && until > now) return true
  }
  return false
}

function compareIncidents(a: DashIncident, b: DashIncident): number {
  const aSup = isSuppressed(a) ? 1 : 0
  const bSup = isSuppressed(b) ? 1 : 0
  if (aSup !== bSup) return aSup - bSup
  const sevA = SEVERITY_RANK[a.effective_severity] ?? 9
  const sevB = SEVERITY_RANK[b.effective_severity] ?? 9
  if (sevA !== sevB) return sevA - sevB
  return Date.parse(b.last_seen) - Date.parse(a.last_seen)
}

async function fetchAllIncidents(signal: AbortSignal): Promise<DashIncident[]> {
  const [workspaces, links] = await Promise.all([listWorkspaces(), safeLinks()])
  if (signal.aborted) return []
  const originOf = buildOriginResolver(links)
  const perWorkspace = await Promise.all(
    workspaces.map(async (ws): Promise<DashIncident[]> => {
      try {
        const res = await listIncidents(ws.id, { status: 'active' })
        const origin = originOf(ws)
        return res.incidents.map((inc) => ({
          ...inc,
          workspaceName: ws.name,
          origin,
        }))
      } catch {
        // One unreachable workspace must not blank the whole panel.
        return []
      }
    }),
  )
  // Dismiss resolves an incident via disposition='benign' (there is no
  // dismissed_at column); drop those from the operator's view even if the
  // status=active list still reports them until the next sweep.
  const flat = perWorkspace.flat().filter((i) => i.disposition !== 'benign')
  flat.sort(compareIncidents)
  return flat
}

const DEFAULT_POLL_MS = 30_000

export function useDashboardIncidents(pollMs = DEFAULT_POLL_MS): UseIncidentsReturn {
  const [state, setState] = useState<IncidentsState>({
    incidents: [], loading: true, error: null,
  })
  const [trigger, setTrigger] = useState(0)
  const refetch = useCallback(() => setTrigger((t) => t + 1), [])

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    fetchAllIncidents(controller.signal)
      .then((incidents) => {
        if (active) setState({ incidents, loading: false, error: null })
      })
      .catch((err: unknown) => {
        if (!active) return
        const message = err instanceof Error ? err.message : 'Failed to load incidents'
        setState((prev) => ({ ...prev, loading: false, error: message }))
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [trigger])

  useInterval(refetch, pollMs)

  const mutate = useCallback(
    (fn: (prev: DashIncident[]) => DashIncident[]) => {
      let snapshot: DashIncident[] = []
      setState((prev) => {
        snapshot = prev.incidents
        return { ...prev, incidents: fn(prev.incidents) }
      })
      return () => setState((prev) => ({ ...prev, incidents: snapshot }))
    },
    [],
  )

  return { ...state, refetch, mutate }
}
