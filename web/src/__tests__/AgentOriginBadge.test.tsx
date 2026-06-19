import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import {
  AgentOriginBadge,
  formatPeerLabel,
} from '@/components/mesh/AgentOriginBadge'

describe('AgentOriginBadge', () => {
  it('renders "local" pill when origin is "local"', () => {
    render(<AgentOriginBadge origin="local" />)
    const el = screen.getByTestId('agent-origin-local')
    expect(el).toBeInTheDocument()
    expect(el).toHaveTextContent('local')
  })

  it('renders "local" pill when origin is missing (legacy daemon)', () => {
    render(<AgentOriginBadge origin={undefined} />)
    expect(screen.getByTestId('agent-origin-local')).toHaveTextContent('local')
  })

  it('renders peer pill with friendly name when known', () => {
    render(
      <AgentOriginBadge
        origin="peer:12D3KooWFooBarBaz"
        peerNames={{ '12D3KooWFooBarBaz': 'peer-laptop' }}
      />,
    )
    const el = screen.getByTestId('agent-origin-peer')
    expect(el).toHaveTextContent('peer:peer-laptop')
  })

  it('falls back to truncated peer id when no display name is known', () => {
    render(<AgentOriginBadge origin="peer:12D3KooWFooBarBazAbCdEf12345678" />)
    const el = screen.getByTestId('agent-origin-peer')
    expect(el.textContent).toMatch(/^peer:/)
    // Short tail must be present, full peer id must not.
    expect(el.textContent).not.toContain('12D3KooWFooBarBazAbCdEf')
  })

  it('snapshot — local badge', () => {
    const { container } = render(<AgentOriginBadge origin="local" />)
    expect(container.firstChild).toMatchInlineSnapshot(`
      <span
        class="inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium bg-emerald-500/10 text-emerald-600 border-emerald-500/30"
        data-testid="agent-origin-local"
        title="local"
      >
        local
      </span>
    `)
  })

  it('snapshot — peer badge with display name', () => {
    const { container } = render(
      <AgentOriginBadge
        origin="peer:12D3KooWAbcdefghij"
        peerNames={{ '12D3KooWAbcdefghij': 'elliot' }}
      />,
    )
    expect(container.firstChild).toMatchInlineSnapshot(`
      <span
        class="inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium bg-muted text-muted-foreground border-border"
        data-testid="agent-origin-peer"
        title="peer:12D3KooWAbcdefghij"
      >
        peer:elliot
      </span>
    `)
  })
})

describe('formatPeerLabel', () => {
  it('returns origin unchanged when not a peer:* origin', () => {
    expect(formatPeerLabel('local')).toBe('local')
  })

  it('uses display name when available', () => {
    expect(formatPeerLabel('peer:abc', { abc: 'air' })).toBe('peer:air')
  })

  it('truncates long peer ids to last 10 chars', () => {
    expect(formatPeerLabel('peer:0123456789ABCDEFGHIJ')).toBe('peer:ABCDEFGHIJ')
  })

  it('keeps short peer ids intact', () => {
    expect(formatPeerLabel('peer:abc')).toBe('peer:abc')
  })
})
