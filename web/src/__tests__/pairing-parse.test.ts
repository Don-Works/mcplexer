import { describe, expect, it } from 'vitest'
import {
  parsePairingInput,
  parsePairingJSON,
  parsePairingURL,
} from '@/components/pairing/parse'

describe('parsePairingURL', () => {
  it('parses a fully-specified mcplexer:// URL', () => {
    const result = parsePairingURL(
      'mcplexer://pair/123456?peer=12D3KooWAbcdef&u=user-1&n=Max%27s%20Air',
    )
    expect(result).toEqual({
      code: '123456',
      peer_id: '12D3KooWAbcdef',
      user_id: 'user-1',
      display_name: "Max's Air",
    })
  })

  it('parses a minimal URL (peer + code only)', () => {
    const result = parsePairingURL('mcplexer://pair/000000?peer=PEERID')
    expect(result).toEqual({
      code: '000000',
      peer_id: 'PEERID',
      user_id: undefined,
      display_name: undefined,
    })
  })

  it('rejects URLs without a 6-digit code', () => {
    expect(parsePairingURL('mcplexer://pair/abc?peer=x')).toBeNull()
    expect(parsePairingURL('mcplexer://pair/?peer=x')).toBeNull()
    expect(parsePairingURL('mcplexer://pair/12345?peer=x')).toBeNull()
    expect(parsePairingURL('mcplexer://pair/1234567?peer=x')).toBeNull()
  })

  it('rejects URLs without a peer query', () => {
    expect(parsePairingURL('mcplexer://pair/123456')).toBeNull()
    expect(parsePairingURL('mcplexer://pair/123456?u=foo')).toBeNull()
  })

  it('rejects non-pairing schemes', () => {
    expect(parsePairingURL('https://example.com/123456?peer=x')).toBeNull()
    expect(parsePairingURL('mcplexer://other/123456?peer=x')).toBeNull()
    expect(parsePairingURL('not-a-url')).toBeNull()
  })

  it('tolerates trailing whitespace + mixed case scheme', () => {
    expect(
      parsePairingURL('  MCPLEXER://pair/123456?peer=ABC  '),
    ).toEqual({
      code: '123456',
      peer_id: 'ABC',
      user_id: undefined,
      display_name: undefined,
    })
  })
})

describe('parsePairingJSON', () => {
  it('parses the QR JSON payload', () => {
    const result = parsePairingJSON(
      '{"code":"654321","peer_id":"12D3KooWxyz","display_name":"dev-mbp"}',
    )
    expect(result).toEqual({
      code: '654321',
      peer_id: '12D3KooWxyz',
      user_id: undefined,
      display_name: 'dev-mbp',
    })
  })

  it('returns null for malformed JSON', () => {
    expect(parsePairingJSON('not json')).toBeNull()
    expect(parsePairingJSON('')).toBeNull()
    expect(parsePairingJSON('null')).toBeNull()
    expect(parsePairingJSON('"just a string"')).toBeNull()
  })

  it('returns null when peer_id is missing', () => {
    expect(parsePairingJSON('{"code":"123456"}')).toBeNull()
  })
})

describe('parsePairingInput', () => {
  it('prefers URL parsing when input looks like a URL', () => {
    const result = parsePairingInput('mcplexer://pair/123456?peer=ABC')
    expect(result?.peer_id).toBe('ABC')
  })

  it('falls back to JSON parsing for QR payloads', () => {
    const result = parsePairingInput('{"code":"123456","peer_id":"DEF"}')
    expect(result?.peer_id).toBe('DEF')
  })

  it('returns null for unparseable input', () => {
    expect(parsePairingInput('garbage')).toBeNull()
  })
})
