import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import { MonitoringPage } from '@/pages/monitoring/MonitoringPage'
import type { RemoteHost } from '@/api/monitoring'

const mocks = vi.hoisted(() => ({
  listAuthScopes: vi.fn(),
  listWorkspaces: vi.fn(),
  request: vi.fn(),
  listWorkers: vi.fn(),
  listChannels: vi.fn(),
  listLogSources: vi.fn(),
  listRemoteHosts: vi.fn(),
  listTemplates: vi.fn(),
  monitoringStatus: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  listAuthScopes: mocks.listAuthScopes,
  listWorkspaces: mocks.listWorkspaces,
  request: mocks.request,
}))

vi.mock('@/api/workers', () => ({
  listWorkers: mocks.listWorkers,
}))

vi.mock('@/api/monitoring', () => ({
  listChannels: mocks.listChannels,
  listLogSources: mocks.listLogSources,
  listRemoteHosts: mocks.listRemoteHosts,
  listTemplates: mocks.listTemplates,
  monitoringStatus: mocks.monitoringStatus,
}))

vi.mock('@/pages/monitoring/RunnerStrip', () => ({
  RunnerStrip: () => <div data-testid="runner-strip" />,
}))
vi.mock('@/pages/monitoring/HostsSection', () => ({
  HostsSection: () => <div data-testid="hosts-section" />,
}))
vi.mock('@/pages/monitoring/SourcesSection', () => ({
  SourcesSection: () => <div data-testid="sources-section" />,
}))
vi.mock('@/pages/monitoring/ChannelsSection', () => ({
  ChannelsSection: () => <div data-testid="channels-section" />,
}))
vi.mock('@/pages/monitoring/TemplatesSection', () => ({
  TemplatesSection: () => <div data-testid="templates-section" />,
}))
vi.mock('@/pages/monitoring/DigestPanel', () => ({
  DigestPanel: () => <div data-testid="digest-panel" />,
}))

const workspace = { id: 'workspace-1', name: 'Example' }

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/monitoring?workspace=invalid-workspace']}>
      <MonitoringPage />
    </MemoryRouter>,
  )
}

describe('MonitoringPage workspace resolution', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.listAuthScopes.mockResolvedValue([])
    mocks.listWorkspaces.mockResolvedValue([workspace])
    mocks.listWorkers.mockResolvedValue([])
    mocks.listChannels.mockResolvedValue([])
    mocks.listLogSources.mockResolvedValue([])
    mocks.listRemoteHosts.mockResolvedValue([])
    mocks.listTemplates.mockResolvedValue({ templates: [], window: '24h' })
    mocks.monitoringStatus.mockResolvedValue({
      gateway_hostname: 'example-gateway',
      runner_enabled: true,
      notify_enabled: false,
    })
    mocks.request.mockResolvedValue([])
  })

  it('gates sections while an invalid workspace is being probed', async () => {
    let resolveProbe!: (hosts: RemoteHost[]) => void
    const probe = new Promise<RemoteHost[]>(resolve => { resolveProbe = resolve })
    let probeSignal: AbortSignal | undefined
    mocks.request.mockImplementation((_path: string, init?: RequestInit) => {
      probeSignal = init?.signal ?? undefined
      return probe
    })

    const { unmount } = renderPage()

    await waitFor(() => expect(mocks.request).toHaveBeenCalled())
    expect(screen.queryByTestId('sources-section')).not.toBeInTheDocument()
    expect(mocks.request).toHaveBeenCalledWith(
      '/remote-hosts?workspace_id=workspace-1',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
      { timeoutMs: 10_000 },
    )

    unmount()
    expect(probeSignal?.aborted).toBe(true)
    resolveProbe([])
  })
})
