import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

import { SourcesSection } from '@/pages/monitoring/SourcesSection'
import { deleteLogSource, updateLogSource } from '@/api/monitoring'
import { toast } from 'sonner'
import type { LogSource, RemoteHost } from '@/api/monitoring'

vi.mock('sonner', () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}))

vi.mock('@/api/monitoring', () => ({
  createLogSource: vi.fn(),
  deleteLogSource: vi.fn(),
  updateLogSource: vi.fn(),
}))

const host: RemoteHost = {
  id: 'host-1',
  workspace_id: 'workspace-1',
  name: 'example-host',
  ssh_user: 'logwatch',
  ssh_host: '203.0.113.10',
  ssh_port: 22,
  auth_scope_id: 'scope-1',
  enabled: true,
  created_at: '',
  updated_at: '',
}

const source: LogSource = {
  id: 'source-1',
  workspace_id: 'workspace-1',
  remote_host_id: host.id,
  name: 'api',
  kind: 'docker',
  selector: 'example-api',
  schedule_spec: '2m',
  max_pull_bytes: 1000,
  retention_mb: 10,
  retention_days: 7,
  enabled: true,
  consecutive_failures: 0,
  created_at: '',
  updated_at: '',
}

const mockedUpdateLogSource = vi.mocked(updateLogSource)
const mockedDeleteLogSource = vi.mocked(deleteLogSource)

function renderSection(refetch = vi.fn()) {
  render(
    <SourcesSection
      workspaceId="workspace-1"
      sources={[source]}
      hosts={[host]}
      refetch={refetch}
    />,
  )
  return refetch
}

describe('SourcesSection mutations and form labels', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockedUpdateLogSource.mockResolvedValue({ ...source })
    mockedDeleteLogSource.mockResolvedValue(undefined)
  })

  it('reports failed enablement changes and refetches authoritative data', async () => {
    mockedUpdateLogSource.mockRejectedValueOnce(new Error('cannot update'))
    const refetch = renderSection()

    fireEvent.click(screen.getByRole('button', { name: 'disable' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('cannot update'))
    expect(refetch).toHaveBeenCalledTimes(1)
    expect(screen.getByText('enabled')).toBeInTheDocument()
  })

  it('closes the delete confirmation and refetches after a backend failure', async () => {
    mockedDeleteLogSource.mockRejectedValueOnce(new Error('cannot delete'))
    const refetch = renderSection()

    fireEvent.click(screen.getByRole('button', { name: 'Delete source api' }))
    fireEvent.click(screen.getByRole('button', { name: /^Delete$/ }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('cannot delete'))
    expect(refetch).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('labels the host and kind selects when adding a source', () => {
    renderSection()

    fireEvent.click(screen.getByRole('button', { name: 'source' }))

    expect(screen.getByLabelText('Log source host')).toBeInTheDocument()
    expect(screen.getByLabelText('Log source kind')).toBeInTheDocument()
  })
})
