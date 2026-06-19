import { describe, expect, it } from 'vitest'
import {
  classifySecretEvent,
  isSecretsActor,
  isSuccessStatus,
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

describe('isSecretsActor', () => {
  it('detects rows emitted by the secret resolver', () => {
    expect(isSecretsActor({ actor_kind: 'secrets' })).toBe(true)
    expect(isSecretsActor({ client_type: 'secrets' })).toBe(true)
    expect(isSecretsActor({ actor_kind: 'user', client_type: 'claude-code' })).toBe(false)
    expect(isSecretsActor({})).toBe(false)
  })
})
