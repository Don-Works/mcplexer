// IndexTypeahead + RefTokenInput — the G4 shared dropdown + create-on-miss.
//
// Covers the load-bearing DESIGN §4.0/§4.1 behaviors:
//   - the dropdown renders frecency-ranked hits from /brain/search as ARIA
//     options with the create-on-miss row always last,
//   - clicking a hit / the create row fires onSelect with the right option,
//   - RefTokenInput's create-on-miss path creates a real stub .md and inserts
//     the returned id as a [[ref]] immediately.
import { afterEach, describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

// Mock the brain browser API surface the components call.
const searchBrain = vi.fn()
const createBrainStub = vi.fn()
vi.mock('@/api/brainBrowser', () => ({
  searchBrain: (...a: unknown[]) => searchBrain(...a),
  createBrainStub: (...a: unknown[]) => createBrainStub(...a),
}))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { IndexTypeahead } from '@/pages/brain/components/IndexTypeahead'
import { RefTokenInput } from '@/pages/brain/components/RefTokenInput'
import type { BrainSearchResult } from '@/api/brainBrowser'

const result = (over: Partial<BrainSearchResult> = {}): BrainSearchResult => ({
  hits: [
    { kind: 'task', id: '01alpha', title: 'Alpha', status: 'review', tags: ['sched'], score: 1, tier: 0 },
    { kind: 'task', id: '01beta', title: 'Beta', status: 'doing', tags: [], score: 1, tier: 1 },
  ],
  fuzzy_off: false,
  create_label: '',
  ...over,
})

afterEach(() => {
  vi.clearAllMocks()
})

describe('IndexTypeahead', () => {
  it('renders ranked hits as ARIA options with create-on-miss last', async () => {
    searchBrain.mockResolvedValue(result())
    render(
      <IndexTypeahead open inline query="al" mode="ref" onSelect={vi.fn()} />,
    )
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    const opts = screen.getAllByRole('option')
    // 2 hits + the create row.
    expect(opts).toHaveLength(3)
    expect(opts.at(-1)?.textContent).toContain('Create "al"')
  })

  it('fires onSelect with the chosen hit option', async () => {
    searchBrain.mockResolvedValue(result())
    const onSelect = vi.fn()
    render(<IndexTypeahead open inline query="al" mode="ref" onSelect={onSelect} />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    fireEvent.mouseDown(screen.getByText('Alpha'))
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect.mock.calls[0][0]).toMatchObject({ id: '01alpha', create: false })
  })

  it('shows create-on-miss even when search fails mid-keystroke', async () => {
    searchBrain.mockRejectedValue(new Error('boom'))
    render(<IndexTypeahead open inline query="newthing" mode="ref" onSelect={vi.fn()} />)
    await waitFor(() =>
      expect(screen.getByText(/Create "newthing"/)).toBeInTheDocument(),
    )
  })
})

describe('RefTokenInput create-on-miss', () => {
  it('creates a real stub and inserts the returned id as a [[ref]]', async () => {
    searchBrain.mockResolvedValue(result({ hits: [] }))
    createBrainStub.mockResolvedValue({ id: '01stub', title: 'fresh' })
    const onChange = vi.fn()
    render(<RefTokenInput refs={[]} onChange={onChange} workspace="ws" />)

    const input = screen.getByRole('combobox')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'fresh' } })

    const createRow = await screen.findByText(/Create "fresh"/)
    fireEvent.mouseDown(createRow)

    await waitFor(() => expect(createBrainStub).toHaveBeenCalledWith('task', 'fresh', 'ws'))
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(['01stub']))
  })
})
