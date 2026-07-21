// GuidanceNudge — the G6 inline guidance affordance (DESIGN §4.4).
//
// Covers the load-bearing behaviors:
//   - renders the nudge message as a single in-field line (role=status, polite
//     live region) — never a popup/modal,
//   - the apply button fires onApply with the nudge,
//   - the dismiss button fires onDismiss.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { GuidanceNudge } from '@/pages/brain/components/GuidanceNudge'
import type { AssistGuidanceNudge } from '@/api/brainBrowser'

const nudge: AssistGuidanceNudge = {
  kind: 'auto-tag',
  message: 'looks like a #scheduler task - add tag?',
  apply: { add_tag: 'scheduler' },
}

describe('GuidanceNudge', () => {
  it('renders the message in a polite live region', () => {
    render(<GuidanceNudge nudge={nudge} onApply={vi.fn()} onDismiss={vi.fn()} />)
    const region = screen.getByRole('status')
    expect(region).toHaveAttribute('aria-live', 'polite')
    expect(screen.getByText(nudge.message)).toBeTruthy()
  })

  it('apply fires onApply with the nudge', () => {
    const onApply = vi.fn()
    render(<GuidanceNudge nudge={nudge} onApply={onApply} onDismiss={vi.fn()} />)
    fireEvent.click(screen.getByText('apply'))
    expect(onApply).toHaveBeenCalledWith(nudge)
  })

  it('dismiss fires onDismiss', () => {
    const onDismiss = vi.fn()
    render(<GuidanceNudge nudge={nudge} onApply={vi.fn()} onDismiss={onDismiss} />)
    fireEvent.click(screen.getByLabelText('dismiss suggestion'))
    expect(onDismiss).toHaveBeenCalledWith(nudge)
  })
})
