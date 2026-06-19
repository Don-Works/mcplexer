import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

import { WorkerEditorPage } from '@/pages/workers/WorkerEditorPage'

vi.mock('sonner', () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}))

vi.mock('@/hooks/use-api', () => ({
  useApi: () => ({
    data: [{ id: 'ws1', name: 'Default' }],
    loading: false,
    error: null,
    refetch: vi.fn(),
  }),
}))

vi.mock('@/api/workers', () => ({
  createWorker: vi.fn().mockResolvedValue({ id: 'w1', name: 'test' }),
  updateWorker: vi.fn().mockResolvedValue({}),
  getWorker: vi.fn(),
  listTools: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  listAuthScopes: vi.fn().mockResolvedValue([]),
  listSkillRegistry: vi.fn().mockResolvedValue([]),
  listWorkspaces: vi.fn().mockResolvedValue([{ id: 'ws1', name: 'Default' }]),
}))

describe('WorkerEditorPage tabbed flow', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the full tab list with stable test ids', () => {
    render(
      <MemoryRouter initialEntries={['/workers/new']}>
        <WorkerEditorPage />
      </MemoryRouter>,
    )

    expect(screen.getByTestId('worker-editor-tabs')).toBeInTheDocument()
    for (const tab of ['basics', 'model', 'prompt', 'schedule', 'tools', 'output', 'execution', 'limits', 'skills']) {
      expect(screen.getByTestId(`worker-editor-tab-${tab}`)).toBeInTheDocument()
    }
  })

  it('defaults to the Basics tab and shows the create form name field', () => {
    render(
      <MemoryRouter initialEntries={['/workers/new']}>
        <WorkerEditorPage />
      </MemoryRouter>,
    )

    expect(screen.getByTestId('worker-name')).toBeInTheDocument()
    expect(screen.getByText(/new worker/i)).toBeInTheDocument()
  })

  it('can switch to the Limits tab and show safety limits content', () => {
    render(
      <MemoryRouter initialEntries={['/workers/new']}>
        <WorkerEditorPage />
      </MemoryRouter>,
    )

    const limitsTab = screen.getByTestId('worker-editor-tab-limits')
    limitsTab.focus()
    fireEvent.keyDown(limitsTab, { key: 'Enter', code: 'Enter' })

    expect(screen.getByText(/safety limits/i)).toBeInTheDocument()
  })

  it('renders the save button once collaborators have loaded', () => {
    render(
      <MemoryRouter initialEntries={['/workers/new']}>
        <WorkerEditorPage />
      </MemoryRouter>,
    )

    expect(screen.getByTestId('worker-save')).toBeInTheDocument()
    expect(screen.getByTestId('worker-save')).toHaveTextContent(/save/i)
  })
})
