// IncidentsPanel data-path tests. The panel's whole job is to union incidents
// from every accessible workspace — including workspaces mirrored from a peer
// over p2p — sort them so the worst is first, and label where each came from.
// These render the real component + real aggregation hook against a mocked API
// layer, so the loop, the origin labelling, and the severity sort are all
// exercised end to end without a live daemon.

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import { IncidentsPanel } from '@/pages/dashboard/incidents-panel'
import type { Workspace, WorkspaceLink } from '@/api/types'
import type { IncidentListResponse, MonitoringIncident } from '@/api/monitoring'

const mocks = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  listWorkspaceLinks: vi.fn(),
  listIncidents: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  listWorkspaces: mocks.listWorkspaces,
  listWorkspaceLinks: mocks.listWorkspaceLinks,
}))

vi.mock('@/api/monitoring', () => ({
  listIncidents: mocks.listIncidents,
  ackIncident: vi.fn(),
  silenceIncident: vi.fn(),
  unsilenceIncident: vi.fn(),
  dismissIncident: vi.fn(),
}))

// Real 30s poll timer is irrelevant to a one-shot render assertion.
vi.mock('@/hooks/use-interval', () => ({ useInterval: () => {} }))

function ws(id: string, name: string): Workspace {
  return {
    id, name, root_path: '/tmp', tags: {}, default_policy: 'allow',
    created_at: '2026-07-01T00:00:00Z', updated_at: '2026-07-01T00:00:00Z',
  }
}

function incident(part: Partial<MonitoringIncident>): MonitoringIncident {
  return {
    id: 'inc', workspace_id: 'ws', class_key: 'absence:x', task_id: 't',
    disposition: 'actionable', severity: 'warn', title: 'something',
    occurrence_count: 1, event_count: 1,
    first_seen: '2026-07-20T12:00:00Z', last_seen: '2026-07-21T09:00:00Z',
    created_at: '2026-07-20T12:00:00Z', updated_at: '2026-07-21T09:00:00Z',
    effective_severity: 'warn', class_kind: 'absence', active: true,
    ...part,
  }
}

function listResponse(incidents: MonitoringIncident[]): IncidentListResponse {
  return { incidents, total: incidents.length, active: incidents.length, class_kinds: [] }
}

function renderPanel() {
  return render(
    <MemoryRouter>
      <IncidentsPanel />
    </MemoryRouter>,
  )
}

describe('IncidentsPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('unions incidents across a local and a peer-mirrored workspace, worst first', async () => {
    mocks.listWorkspaces.mockResolvedValue([ws('ws-local', 'acme'), ws('ws-peer', 'peer-mirror')])
    const link: WorkspaceLink = {
      peer_id: 'peer-1', local_workspace_id: 'ws-peer',
      remote_workspace_id: 'r1', remote_workspace_name: 'beta-corp',
    }
    mocks.listWorkspaceLinks.mockResolvedValue([link])
    mocks.listIncidents.mockImplementation((wsId: string) =>
      Promise.resolve(
        listResponse(
          wsId === 'ws-peer'
            ? [incident({
                id: 'p1', workspace_id: 'ws-peer', title: 'backup job stopped',
                severity: 'critical', effective_severity: 'critical',
              })]
            : [incident({
                id: 'l1', workspace_id: 'ws-local', title: 'orders sync stalled',
                severity: 'warn', effective_severity: 'warn',
              })],
        ),
      ),
    )

    renderPanel()

    await waitFor(() => expect(screen.getByText('backup job stopped')).toBeTruthy())
    // Both incidents surfaced from their respective workspaces.
    expect(screen.getByText('orders sync stalled')).toBeTruthy()
    // Per-workspace endpoint hit once each — no cross-workspace endpoint needed.
    expect(mocks.listIncidents).toHaveBeenCalledWith('ws-local', { status: 'active' })
    expect(mocks.listIncidents).toHaveBeenCalledWith('ws-peer', { status: 'active' })

    // Origin labels: the peer mirror shows its peer's name, the local its own.
    const panel = screen.getByTestId('dash-incidents')
    expect(within(panel).getByText('beta-corp')).toBeTruthy()
    expect(within(panel).getByText('acme')).toBeTruthy()
    // Header summarises the critical count across everything.
    expect(within(panel).getByText('1 critical · 2')).toBeTruthy()

    // Severity sort: the critical row renders before the warn row.
    const titles = within(panel).getAllByRole('link').map((a) => a.textContent)
    expect(titles.indexOf('backup job stopped')).toBeLessThan(
      titles.indexOf('orders sync stalled'),
    )
  })

  it('renders a calm all-clear line when no workspace has incidents', async () => {
    mocks.listWorkspaces.mockResolvedValue([ws('ws-local', 'acme')])
    mocks.listWorkspaceLinks.mockResolvedValue([])
    mocks.listIncidents.mockResolvedValue(listResponse([]))

    renderPanel()

    await waitFor(() =>
      expect(screen.getByText(/All clear across every/)).toBeTruthy(),
    )
    const panel = screen.getByTestId('dash-incidents')
    expect(within(panel).getByText('clear')).toBeTruthy()
  })

  it('keeps the panel alive when one workspace read fails', async () => {
    mocks.listWorkspaces.mockResolvedValue([ws('ws-a', 'acme'), ws('ws-b', 'beta')])
    mocks.listWorkspaceLinks.mockResolvedValue([])
    mocks.listIncidents.mockImplementation((wsId: string) =>
      wsId === 'ws-b'
        ? Promise.reject(new Error('workspace unreachable'))
        : Promise.resolve(
            listResponse([
              incident({ id: 'a1', workspace_id: 'ws-a', title: 'disk filling up' }),
            ]),
          ),
    )

    renderPanel()

    await waitFor(() => expect(screen.getByText('disk filling up')).toBeTruthy())
  })

  it('prefers silence_active over a stale silenced_until so a pierced silence still nags', async () => {
    mocks.listWorkspaces.mockResolvedValue([ws('ws-local', 'acme')])
    mocks.listWorkspaceLinks.mockResolvedValue([])
    const future = new Date(Date.now() + 3_600_000).toISOString()
    mocks.listIncidents.mockResolvedValue(
      listResponse([
        // Escalated past the floor it was silenced at: the window is still open
        // (silenced_until in the future) but the daemon pierced it, so the
        // derived flag says it is nagging again.
        incident({
          id: 'pierced', workspace_id: 'ws-local', title: 'db replication broke',
          severity: 'critical', effective_severity: 'critical',
          silenced_until: future, silence_active: false, suppressed: false,
        }),
        // A genuinely-silenced, lower-severity incident: still muted.
        incident({
          id: 'muted', workspace_id: 'ws-local', title: 'nightly report slow',
          severity: 'warn', effective_severity: 'warn',
          silenced_until: future, silence_active: true, suppressed: true,
        }),
      ]),
    )

    renderPanel()

    const panel = await waitFor(() => screen.getByTestId('dash-incidents'))
    // The pierced incident counts as a live critical (not silenced) even though
    // silenced_until is still in the future — the raw column alone would hide it.
    expect(within(panel).getByText('db replication broke')).toBeTruthy()
    expect(within(panel).getByText('1 critical · 2')).toBeTruthy()
    // Exactly one row wears a "silenced …" chip — the genuinely muted one.
    expect(within(panel).queryAllByText(/^silenced/)).toHaveLength(1)
    // The live critical sorts above the muted/suppressed warn.
    const titles = within(panel).getAllByRole('link').map((a) => a.textContent)
    expect(titles.indexOf('db replication broke')).toBeLessThan(
      titles.indexOf('nightly report slow'),
    )
  })

  it('drops incidents dismissed to disposition=benign', async () => {
    mocks.listWorkspaces.mockResolvedValue([ws('ws-local', 'acme')])
    mocks.listWorkspaceLinks.mockResolvedValue([])
    mocks.listIncidents.mockResolvedValue(
      listResponse([
        incident({ id: 'live', workspace_id: 'ws-local', title: 'queue backing up' }),
        // Dismiss resolves via disposition=benign (there is no dismissed_at);
        // the status=active list may still return it until the next sweep.
        incident({
          id: 'gone', workspace_id: 'ws-local', title: 'flaky check settled',
          disposition: 'benign',
        }),
      ]),
    )

    renderPanel()

    await waitFor(() => expect(screen.getByText('queue backing up')).toBeTruthy())
    expect(screen.queryByText('flaky check settled')).toBeNull()
    expect(within(screen.getByTestId('dash-incidents')).getByText('1')).toBeTruthy()
  })
})
