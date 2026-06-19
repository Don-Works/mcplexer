// FrontmatterForm + ValidationBanner — the G3 field-mapping engine.
//
// These cover the load-bearing DESIGN §3.2 / §3.7 behaviors:
//   - status renders as a ToggleGroup populated from the workspace vocab, with
//     an off-vocab value folded in + flagged "off-vocab" (never free-text),
//   - the 422 field error renders inline at the offending control with the
//     allowed-vocab hint,
//   - composes + assignee controls exist and round-trip onChange,
//   - the fact-only "Valid from" field appears only for kind=fact,
//   - the ValidationBanner one-click fix snaps the field to the first allowed
//     vocab value.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

import {
  TaskFrontmatterForm,
  MemoryFrontmatterForm,
} from '@/pages/brain/components/FrontmatterForm'
import { ValidationBanner } from '@/pages/brain/components/ValidationBanner'
import type { BrainTaskRecord, BrainMemoryRecord } from '@/api/brainBrowser'

const task = (over: Partial<BrainTaskRecord> = {}): BrainTaskRecord => ({
  id: '01task',
  workspace: 'ws',
  title: 'T',
  status: 'open',
  tags: [],
  pinned: false,
  description: '',
  ...over,
})

const memory = (over: Partial<BrainMemoryRecord> = {}): BrainMemoryRecord => ({
  id: '01mem',
  kind: 'note',
  name: 'm',
  workspace: 'ws',
  tags: [],
  pinned: false,
  content: '',
  ...over,
})

describe('TaskFrontmatterForm', () => {
  it('renders status as a ToggleGroup from the vocab (not free text)', () => {
    render(
      <TaskFrontmatterForm
        rec={task({ status: 'doing' })}
        vocab={['open', 'doing', 'review', 'done']}
        err={null}
        onChange={vi.fn()}
      />,
    )
    // Each vocab value is a selectable radio item.
    for (const s of ['open', 'doing', 'review', 'done']) {
      expect(screen.getByRole('radio', { name: s })).toBeInTheDocument()
    }
    expect(screen.getByRole('radio', { name: 'doing' })).toHaveAttribute('aria-checked', 'true')
  })

  it('folds an off-vocab status into the control and warns', () => {
    render(
      <TaskFrontmatterForm
        rec={task({ status: 'in-progres' })}
        vocab={['open', 'doing', 'done']}
        err={null}
        onChange={vi.fn()}
      />,
    )
    expect(screen.getByRole('radio', { name: 'in-progres' })).toBeInTheDocument()
    expect(screen.getByText('current value off-vocab')).toBeInTheDocument()
  })

  it('renders an inline 422 error with the allowed-vocab hint at the status control', () => {
    render(
      <TaskFrontmatterForm
        rec={task()}
        vocab={['open', 'doing']}
        err={{ field: 'status', message: 'status is not in vocab', allowed: ['open', 'doing'] }}
        onChange={vi.fn()}
      />,
    )
    expect(screen.getByText('status is not in vocab')).toBeInTheDocument()
    expect(screen.getByText(/allowed: open · doing/)).toBeInTheDocument()
  })

  it('associates each field label with its control (a11y §7)', () => {
    render(<TaskFrontmatterForm rec={task({ title: 'hello' })} vocab={['open']} err={null} onChange={vi.fn()} />)
    // Native control: the Title input is reachable by its accessible name, which
    // only resolves when <Label htmlFor> points at the input's id.
    const title = screen.getByLabelText('Title')
    expect((title as HTMLInputElement).value).toBe('hello')
    // Composite control: the Status ToggleGroup root carries an accessible name
    // via aria-labelledby (htmlFor cannot reach a radiogroup).
    expect(screen.getByRole('group', { name: 'Status' })).toBeInTheDocument()
    // Token input: the Tags combobox is labelled via aria-labelledby too.
    expect(screen.getByRole('combobox', { name: 'Tags' })).toBeInTheDocument()
  })

  it('exposes composes + assignee controls and round-trips composes onChange', () => {
    const onChange = vi.fn()
    render(<TaskFrontmatterForm rec={task()} vocab={[]} err={null} onChange={onChange} />)
    expect(screen.getByText('Composes (child records)')).toBeInTheDocument()
    expect(screen.getByText('Assignee')).toBeInTheDocument()
    // The composes field is the typeahead-backed RefTokenInput; the bare
    // comma-commit path (no dropdown) round-trips a literal id.
    const input = screen.getByPlaceholderText('link a record… [[')
    fireEvent.change(input, { target: { value: '01child' } })
    fireEvent.keyDown(input, { key: ',' })
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ composes: ['01child'] }))
  })
})

describe('MemoryFrontmatterForm', () => {
  it('hides the Valid-from field for notes and shows it for facts', () => {
    const { rerender } = render(
      <MemoryFrontmatterForm rec={memory({ kind: 'note' })} err={null} onChange={vi.fn()} />,
    )
    expect(screen.queryByText('Valid from')).not.toBeInTheDocument()
    rerender(<MemoryFrontmatterForm rec={memory({ kind: 'fact' })} err={null} onChange={vi.fn()} />)
    expect(screen.getByText('Valid from')).toBeInTheDocument()
    expect(screen.getByText('Currently valid (valid from now)')).toBeInTheDocument()
  })
})

describe('ValidationBanner', () => {
  it('snaps the offending field to the first allowed vocab value on one-click fix', () => {
    const onFix = vi.fn()
    render(
      <ValidationBanner
        message='status "in-progres" is not in this workspace vocab'
        field="status"
        allowed={['doing', 'done']}
        onFix={onFix}
      />,
    )
    expect(
      screen.getByText('not indexed: your agent cannot see this record yet'),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /set "doing"/ }))
    expect(onFix).toHaveBeenCalledWith('status', 'doing')
  })

  it('omits the fix button when there is no allowed vocab', () => {
    render(<ValidationBanner message="some other failure" />)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
