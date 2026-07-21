import { describe, expect, it } from 'vitest'
import {
  classifySecretEvent,
  isSecretsActor,
  isSuccessStatus,
  liveRowMatchesLocalFilter,
  normalizeStatus,
} from '@/lib/audit-semantics'

describe('normalizeStatus', () => {
  it('treats "ok" the same as "success" (secrets resolver writes "ok")', () => {
    expect(normalizeStatus('ok')).toBe('success')
    expect(normalizeStatus('success')).toBe('success')
    expect(isSuccessStatus('ok')).toBe(true)
    expect(isSuccessStatus('success')).toBe(true)
  })

  it('preserves error and blocked, and treats unknown as error', () => {
    expect(normalizeStatus('error')).toBe('error')
    expect(normalizeStatus('blocked')).toBe('blocked')
    expect(normalizeStatus('weird')).toBe('error')
    expect(normalizeStatus(undefined)).toBe('error')
    expect(isSuccessStatus('error')).toBe(false)
    expect(isSuccessStatus('blocked')).toBe(false)
  })
})

describe('classifySecretEvent', () => {
  it('marks secret.list as calm enumeration — key names only, no value read', () => {
    const sem = classifySecretEvent('secret.list')
    expect(sem).not.toBeNull()
    expect(sem!.op).toBe('enumerate')
    expect(sem!.tone).toBe('info')
    expect(sem!.label).toBe('Enumeration')
    expect(sem!.blurb.toLowerCase()).toContain('no secret value')
  })

  it('marks secret.read as attention-grade decryption', () => {
    const sem = classifySecretEvent('secret.read')
    expect(sem!.op).toBe('decrypt')
    expect(sem!.tone).toBe('notice')
    expect(sem!.label).toBe('Decryption')
  })

  it('classifies write and delete, and returns null for non-secret tools', () => {
    expect(classifySecretEvent('secret.write')!.op).toBe('store')
    expect(classifySecretEvent('secret.delete')!.op).toBe('delete')
    expect(classifySecretEvent('freeagent__list_invoices')).toBeNull()
    expect(classifySecretEvent('mcpx__execute_code')).toBeNull()
  })
})

describe('liveRowMatchesLocalFilter', () => {
  const row = (over: Partial<{ timestamp: string; latency_ms: number; cache_hit: boolean }> = {}) => ({
    timestamp: '2026-07-01T12:00:00.000Z',
    latency_ms: 500,
    cache_hit: false,
    ...over,
  })

  it('passes when no client-only dims are set', () => {
    expect(liveRowMatchesLocalFilter(row(), {})).toBe(true)
  })

  it('filters on cache_hit (both directions)', () => {
    expect(liveRowMatchesLocalFilter(row({ cache_hit: true }), { cache_hit: true })).toBe(true)
    expect(liveRowMatchesLocalFilter(row({ cache_hit: false }), { cache_hit: true })).toBe(false)
    expect(liveRowMatchesLocalFilter(row({ cache_hit: false }), { cache_hit: false })).toBe(true)
    expect(liveRowMatchesLocalFilter(row({ cache_hit: true }), { cache_hit: false })).toBe(false)
  })

  it('drops rows below the min_latency_ms floor, keeps rows at or above it', () => {
    expect(liveRowMatchesLocalFilter(row({ latency_ms: 999 }), { min_latency_ms: 1000 })).toBe(false)
    expect(liveRowMatchesLocalFilter(row({ latency_ms: 1000 }), { min_latency_ms: 1000 })).toBe(true)
    expect(liveRowMatchesLocalFilter(row({ latency_ms: 1500 }), { min_latency_ms: 1000 })).toBe(true)
  })

  it('enforces the after/before time bounds', () => {
    const r = row({ timestamp: '2026-07-01T12:00:00.000Z' })
    // after = lower bound: row must be >= after
    expect(liveRowMatchesLocalFilter(r, { after: '2026-07-01T11:00:00.000Z' })).toBe(true)
    expect(liveRowMatchesLocalFilter(r, { after: '2026-07-01T13:00:00.000Z' })).toBe(false)
    // before = upper bound: row must be <= before
    expect(liveRowMatchesLocalFilter(r, { before: '2026-07-01T13:00:00.000Z' })).toBe(true)
    expect(liveRowMatchesLocalFilter(r, { before: '2026-07-01T11:00:00.000Z' })).toBe(false)
  })

  it('treats an unparseable bound as no constraint (matches the gateway parse)', () => {
    expect(liveRowMatchesLocalFilter(row(), { after: 'not-a-date' })).toBe(true)
    expect(liveRowMatchesLocalFilter(row(), { before: 'not-a-date' })).toBe(true)
  })

  it('requires every set dim to pass (AND semantics)', () => {
    // cache matches but latency fails -> overall fail
    expect(
      liveRowMatchesLocalFilter(row({ cache_hit: true, latency_ms: 10 }), {
        cache_hit: true,
        min_latency_ms: 1000,
      }),
    ).toBe(false)
  })
})

describe('isSecretsActor', () => {
  it('detects rows emitted by the secret resolver', () => {
    expect(isSecretsActor({ actor_kind: 'secrets' })).toBe(true)
    expect(isSecretsActor({ client_type: 'secrets' })).toBe(true)
    expect(isSecretsActor({ actor_kind: 'user', client_type: 'claude-code' })).toBe(false)
    expect(isSecretsActor({})).toBe(false)
  })
})
