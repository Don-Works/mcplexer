import { describe, expect, it } from 'vitest'
import { humaniseScopeName, scopeLabel } from '@/lib/scope-label'

describe('humaniseScopeName', () => {
  it('strips oauth + agent_ prefix from real-world slugs', () => {
    expect(humaniseScopeName('clickup_oauth_agent_example_workspace')).toBe('Clickup Example Workspace')
    expect(humaniseScopeName('linear_oauth_gateway_linear')).toBe('Linear Linear')
  })

  it('title-cases and despaces dashes + underscores', () => {
    expect(humaniseScopeName('github-token')).toBe('Github Token')
    expect(humaniseScopeName('openai_api_key')).toBe('Openai Api Key')
  })

  it('drops client_credentials + oauth2 noise tokens', () => {
    expect(humaniseScopeName('mything_client_credentials')).toBe('Mything')
    expect(humaniseScopeName('mything_oauth2')).toBe('Mything')
  })

  it('falls back to original when humanisation collapses to empty', () => {
    expect(humaniseScopeName('oauth')).toBe('oauth')
    expect(humaniseScopeName('')).toBe('')
  })

  it('preserves already-human-readable names (caps / spaces / parens)', () => {
    expect(humaniseScopeName('FreeAgent OAuth')).toBe('FreeAgent OAuth')
    expect(humaniseScopeName('Aikido Client Credentials')).toBe('Aikido Client Credentials')
    expect(humaniseScopeName('Intervals Pro (Local)')).toBe('Intervals Pro (Local)')
    expect(humaniseScopeName('Paddle Production API Key')).toBe('Paddle Production API Key')
  })
})

describe('scopeLabel', () => {
  it('prefers display_name when present', () => {
    expect(scopeLabel({ name: 'gh_token', display_name: 'GitHub' })).toBe('GitHub')
  })

  it('falls back to humanised name when display_name is empty', () => {
    expect(scopeLabel({ name: 'gh_token', display_name: '' })).toBe('Gh Token')
  })

  it('treats whitespace-only display_name as unset', () => {
    expect(scopeLabel({ name: 'gh_token', display_name: '   ' })).toBe('Gh Token')
  })
})
