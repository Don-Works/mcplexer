import { describe, expect, it } from 'vitest'
import {
  arbitratePulse,
  guidanceRank,
  topNudge,
} from '@/pages/brain/components/pulseArbiter'
import type {
  AssistGuidanceNudge,
  AssistMemoryCandidate,
} from '@/api/brainBrowser'

const nudge = (kind: string): AssistGuidanceNudge => ({
  kind,
  message: kind,
  apply: {},
})
const candidate = (hash: string): AssistMemoryCandidate => ({
  text: hash,
  kind: 'note',
  signal: 'decision-with-rationale',
  content_hash: hash,
})

describe('guidanceRank', () => {
  it('ranks missing-criteria above auto-tag', () => {
    expect(guidanceRank('missing-acceptance-criteria')).toBeGreaterThan(
      guidanceRank('auto-tag'),
    )
  })
  it('ranks an unknown kind lowest but above zero', () => {
    expect(guidanceRank('something-new')).toBe(10)
  })
})

describe('topNudge', () => {
  it('returns the highest-ranked nudge', () => {
    const got = topNudge([nudge('auto-tag'), nudge('missing-acceptance-criteria'), nudge('entity-extraction')])
    expect(got?.kind).toBe('missing-acceptance-criteria')
  })
  it('returns null for an empty list', () => {
    expect(topNudge([])).toBeNull()
  })
})

describe('arbitratePulse — one-pulse-per-record law', () => {
  it('gives no pulse when nothing is present', () => {
    expect(arbitratePulse([], [])).toEqual({ owner: 'none', nudge: null })
  })

  it('memory candidate alone owns the pulse', () => {
    expect(arbitratePulse([candidate('a')], [])).toEqual({ owner: 'memory', nudge: null })
  })

  it('guidance alone owns the pulse and returns the top nudge', () => {
    const d = arbitratePulse([], [nudge('auto-tag'), nudge('missing-acceptance-criteria')])
    expect(d.owner).toBe('guidance')
    expect(d.nudge?.kind).toBe('missing-acceptance-criteria')
  })

  it('a high-bar memory candidate ALWAYS outranks any guidance nudge', () => {
    // Both present: the memory candidate (decision-with-rationale) wins the
    // single pulse even against the strongest guidance kind. The nudge is still
    // returned so the GUI renders it without a pulse.
    const d = arbitratePulse([candidate('a')], [nudge('missing-acceptance-criteria')])
    expect(d.owner).toBe('memory')
    expect(d.nudge?.kind).toBe('missing-acceptance-criteria')
  })
})
