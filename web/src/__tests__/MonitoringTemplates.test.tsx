import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ackTemplate } from '@/api/monitoring'
import type { MonitoringTemplate } from '@/api/monitoring'
import { TemplatesSection } from '@/pages/monitoring/TemplatesSection'
import { toast } from 'sonner'

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('@/api/monitoring', () => ({
  ackTemplate: vi.fn(),
}))

const template: MonitoringTemplate = {
  id: 'template-1',
  source_id: 'source-1',
  source_name: 'api',
  masked: 'connection failed host=<*>',
  severity: 'error',
  count: 2,
  window_lines: 2,
  first_seen: '2026-07-15T12:00:00Z',
  last_seen: '2026-07-15T12:01:00Z',
  sample_first: 'connection failed host=db-1',
  sample_last: 'connection failed host=db-2',
  acked: false,
  new: true,
}

const mockedAck = vi.mocked(ackTemplate)

function renderSection(refetch = vi.fn()) {
  render(
    <TemplatesSection
      workspaceId="workspace A"
      templates={[template]}
      refetch={refetch}
    />,
  )
  return refetch
}

describe('TemplatesSection acknowledgements', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockedAck.mockResolvedValue({ acked: true })
  })

  it('sends the owning workspace and refetches after success', async () => {
    const refetch = renderSection()
    fireEvent.click(screen.getByRole('button', { name: /Acknowledge template/ }))

    await waitFor(() => expect(mockedAck).toHaveBeenCalledWith(
      'template-1', 'workspace A', 'acked from Monitoring page',
    ))
    expect(toast.success).toHaveBeenCalledWith('template acked')
    expect(refetch).toHaveBeenCalledTimes(1)
  })

  it('surfaces a failed ack and still reloads authoritative state', async () => {
    mockedAck.mockRejectedValueOnce(new Error('ack refused'))
    const refetch = renderSection()
    fireEvent.click(screen.getByRole('button', { name: /Acknowledge template/ }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('ack refused'))
    expect(refetch).toHaveBeenCalledTimes(1)
  })
})
