import { describe, it, expect } from 'vitest'
import { classifyAll, peerConnectedFromModes } from '@/components/mesh/DisconnectedPeersBanner'
import type { PeerRow } from '@/components/pairing/api'
import type { MeshAgent, P2PPeerMode } from '@/api/types'

function peer(id: string, name = '', opts: Partial<PeerRow> = {}): PeerRow {
  return {
    peer_id: id,
    display_name: name,
    paired_at: '2026-01-01T00:00:00Z',
    trust_level: 1,
    scopes: ['mesh'],
    ...opts,
  }
}

function agent(origin: string): MeshAgent {
  return {
    session_id: 's',
    name: '',
    role: '',
    client_type: 'p2p',
    model_hint: '',
    last_seen_at: '2026-05-01T00:00:00Z',
    origin,
  }
}

function mode(p: string, m: P2PPeerMode['mode']): P2PPeerMode {
  return { peer: p, mode: m }
}

describe('classifyAll', () => {
  it('returns empty when no paired peers', () => {
    expect(classifyAll([], [], [])).toEqual([])
  })

  it('flags a paired peer with no last_seen as never_seen', () => {
    const out = classifyAll([peer('12D3peer-laptop', 'peer-laptop')], [], [])
    expect(out).toHaveLength(1)
    expect(out[0].peer_id).toBe('12D3peer-laptop')
    expect(out[0].display_name).toBe('peer-laptop')
    expect(out[0].tier).toBe('never_seen')
  })

  it('treats a paired peer last-seen recently as reconnecting (soft)', () => {
    const recent = new Date(Date.now() - 60_000).toISOString()
    const out = classifyAll([peer('p1', 'air', { last_seen: recent })], [], [])
    expect(out).toHaveLength(1)
    expect(out[0].tier).toBe('reconnecting')
  })

  it('treats a paired peer last-seen 10min ago as offline (loud)', () => {
    const old = new Date(Date.now() - 10 * 60_000).toISOString()
    const out = classifyAll([peer('p1', 'air', { last_seen: old })], [], [])
    expect(out).toHaveLength(1)
    expect(out[0].tier).toBe('offline')
    expect(out[0].ageMin).toBeGreaterThanOrEqual(10)
  })

  it('hides peers with an active libp2p connection', () => {
    expect(
      classifyAll([peer('p1', 'air')], [mode('p1', 'direct')], []),
    ).toEqual([])
  })

  it('hides peers we have an active mesh agent for', () => {
    expect(
      classifyAll([peer('p2', 'air')], [], [agent('peer:p2')]),
    ).toEqual([])
  })

  it('treats live mode "none" same as missing — peer still classified', () => {
    const out = classifyAll([peer('p3', 'air')], [mode('p3', 'none')], [])
    expect(out).toHaveLength(1)
    expect(out[0].peer_id).toBe('p3')
  })

  it('skips revoked peers entirely', () => {
    expect(
      classifyAll([peer('p4', 'air', { revoked_at: '2026-04-01T00:00:00Z' })], [], []),
    ).toEqual([])
  })

  it('local-origin agents do not count as reaching a peer', () => {
    const out = classifyAll([peer('p5', 'air')], [], [agent('local')])
    expect(out).toHaveLength(1)
  })
})

describe('peerConnectedFromModes', () => {
  it('returns false when peer absent', () => {
    expect(peerConnectedFromModes('p1', [])).toBe(false)
  })
  it('returns false when mode is none', () => {
    expect(peerConnectedFromModes('p1', [mode('p1', 'none')])).toBe(false)
  })
  it('returns true for direct/hole-punched/relay', () => {
    expect(peerConnectedFromModes('p1', [mode('p1', 'direct')])).toBe(true)
    expect(peerConnectedFromModes('p1', [mode('p1', 'hole-punched')])).toBe(true)
    expect(peerConnectedFromModes('p1', [mode('p1', 'via-relay')])).toBe(true)
  })
})
