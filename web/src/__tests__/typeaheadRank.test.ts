// typeaheadRank — the pure ranking + create-on-miss + mode-grammar logic
// behind the shared IndexTypeahead (DESIGN §4.0/§4.1). Covers: ref options keep
// the server tier order + append create-on-miss last, an exact title suppresses
// the create row, tag options dedup + prefix-rank + create-on-miss, and the
// mode prefix grammar ([[ / # / @ / >).
import { describe, it, expect } from 'vitest'
import {
  rankRefOptions,
  rankTagOptions,
  detectMode,
} from '@/pages/brain/components/typeaheadRank'
import type { BrainSearchHit } from '@/api/brainBrowser'

const hit = (over: Partial<BrainSearchHit> = {}): BrainSearchHit => ({
  kind: 'task',
  id: '01abc',
  title: 'Re-arm cron',
  status: 'review',
  workspace: 'ws',
  tags: [],
  score: 1,
  tier: 0,
  ...over,
})

describe('rankRefOptions', () => {
  it('maps hits, keeps server order, and appends create-on-miss LAST', () => {
    const opts = rankRefOptions(
      [hit({ id: 'a', title: 'Alpha', tier: 0 }), hit({ id: 'b', title: 'Beta', tier: 1 })],
      're-arm',
    )
    expect(opts.map((o) => o.id)).toEqual(['a', 'b', 're-arm'])
    expect(opts[0].create).toBeFalsy()
    expect(opts.at(-1)?.create).toBe(true)
    expect(opts.at(-1)?.sub).toBe('create')
  })

  it('renders the ref meta line as "<kind> · <status>"', () => {
    const [o] = rankRefOptions([hit({ status: 'doing' })], '')
    expect(o.sub).toBe('task · doing')
  })

  it('suppresses create-on-miss when an exact title already exists', () => {
    const opts = rankRefOptions([hit({ title: 'Spec the Brain' })], 'spec the brain')
    expect(opts.some((o) => o.create)).toBe(false)
  })

  it('omits create-on-miss for an empty query', () => {
    const opts = rankRefOptions([hit()], '')
    expect(opts.some((o) => o.create)).toBe(false)
  })

  it('caps the hit list at the limit (before the create row)', () => {
    const many = Array.from({ length: 20 }, (_, i) => hit({ id: `id${i}`, title: `T${i}` }))
    const opts = rankRefOptions(many, 'zzz', 5)
    // 5 hits + the create row.
    expect(opts).toHaveLength(6)
    expect(opts.at(-1)?.create).toBe(true)
  })
})

describe('rankTagOptions', () => {
  it('dedups tags case-insensitively and prefix-ranks them', () => {
    const opts = rankTagOptions(
      [hit({ tags: ['scheduler', 'Scheduler', 'bug'] }), hit({ tags: ['scope'] })],
      'sc',
    )
    const tags = opts.filter((o) => !o.create).map((o) => o.id.toLowerCase())
    // scheduler + scope both prefix "sc"; bug does not match.
    expect(tags).toContain('scheduler')
    expect(tags).toContain('scope')
    expect(tags).not.toContain('bug')
    // no duplicate of scheduler from the case variant.
    expect(tags.filter((t) => t === 'scheduler')).toHaveLength(1)
  })

  it('appends create-on-miss for a brand-new tag', () => {
    const opts = rankTagOptions([hit({ tags: ['bug'] })], 'regression')
    const create = opts.at(-1)
    expect(create?.create).toBe(true)
    expect(create?.id).toBe('regression')
    expect(create?.label).toBe('Create #regression')
  })

  it('suppresses create-on-miss when the exact tag already exists', () => {
    const opts = rankTagOptions([hit({ tags: ['regression'] })], 'regression')
    expect(opts.some((o) => o.create)).toBe(false)
  })
})

describe('detectMode', () => {
  it('parses the mono prefix grammar', () => {
    expect(detectMode('[[re-arm')).toEqual({ mode: 'ref', text: 're-arm' })
    expect(detectMode('#sched')).toEqual({ mode: 'tag', text: 'sched' })
    expect(detectMode('@acme')).toEqual({ mode: 'workspace', text: 'acme' })
    expect(detectMode('> new task')).toEqual({ mode: 'cmd', text: 'new task' })
  })

  it('returns null for the no-prefix filter mode', () => {
    expect(detectMode('audit')).toBeNull()
    expect(detectMode('')).toBeNull()
  })
})
