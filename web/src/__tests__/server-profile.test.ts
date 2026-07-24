import { describe, expect, it } from 'vitest'
import {
  hasCapability,
  isServerProfile,
  normalizeServerProfile,
  serverHomePath,
  serverProfileLabel,
} from '@/lib/server-profile'

describe('server profiles', () => {
  it('recognizes core as a server profile instead of falling back to full', () => {
    expect(normalizeServerProfile(' CORE ')).toBe('core')
    expect(isServerProfile({ server_profile: 'core' })).toBe(true)
    expect(serverProfileLabel({ server_profile: 'core' })).toBe('Core gateway')
    expect(serverHomePath({ server_profile: 'core' })).toBe('/')
  })

  it('uses the daemon capability map for core navigation', () => {
    const system = {
      server_profile: 'core',
      capabilities: {
        downstreams: true,
        memory: false,
        server_settings: true,
        signals: true,
      },
    }

    expect(hasCapability(system, 'downstreams')).toBe(true)
    expect(hasCapability(system, 'memory')).toBe(false)
  })

  it('falls back conservatively when a core daemon omits capabilities', () => {
    const system = { server_profile: 'core' }

    expect(hasCapability(system, 'server_settings')).toBe(true)
    expect(hasCapability(system, 'signals')).toBe(true)
    expect(hasCapability(system, 'workers')).toBe(false)
  })
})
