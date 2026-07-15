import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SecretPrompt } from '@/hooks/use-secret-prompt-stream'
import { WaitingSecretsSection } from '@/pages/approvals/WaitingSecretsSection'

function prompt(expiresAt: string): SecretPrompt {
  return {
    id: 'prompt-1',
    reason: 'Authenticate a test service',
    label: 'TEST_TOKEN',
    requester: 'test-requester',
    status: 'pending',
    expires_at: expiresAt,
    created_at: '2026-07-15T12:00:00Z',
  }
}

describe('WaitingSecretsSection', () => {
  it('presents an urgent countdown with a visible non-color cue', () => {
    const now = new Date('2026-07-15T12:00:00Z').getTime()
    render(
      <WaitingSecretsSection
        prompts={[prompt('2026-07-15T12:00:20Z')]}
        now={now}
      />,
    )

    expect(screen.getByText('Urgent: 0:20 left')).toBeInTheDocument()
    expect(screen.getByText('Requested by test-requester')).toBeInTheDocument()
  })

  it('does not label a non-urgent countdown as urgent', () => {
    const now = new Date('2026-07-15T12:00:00Z').getTime()
    render(
      <WaitingSecretsSection
        prompts={[prompt('2026-07-15T12:01:00Z')]}
        now={now}
      />,
    )

    expect(screen.getByText('1:00 left')).toBeInTheDocument()
    expect(screen.queryByText(/Urgent:/)).not.toBeInTheDocument()
  })
})
