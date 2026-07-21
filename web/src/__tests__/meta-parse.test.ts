import { describe, expect, it } from 'vitest'
import { parseWorkContext, readMetaList } from '@/api/tasks'

// Regression: the backend writes the task `meta` column as a JSON object
// since migration 072 (e.g. {"composed_by":"<id>"}). The frontend parsers
// were frontmatter-only, so composes/composed_by and work-context fields
// went invisible — the composition tree rendered flat. Both shapes must
// resolve identically while the post-072 backfill rolls forward.
describe('readMetaList — dual-read JSON + legacy frontmatter', () => {
  it('reads a scalar string value from JSON meta (single compose link)', () => {
    const meta = '{"composed_by":"01ABCPARENT"}'
    expect(readMetaList(meta, 'composed_by')).toEqual(['01ABCPARENT'])
  })

  it('reads an array value from JSON meta (multiple children)', () => {
    const meta = '{"composes":["01CHILDA","01CHILDB"]}'
    expect(readMetaList(meta, 'composes')).toEqual(['01CHILDA', '01CHILDB'])
  })

  it('still reads legacy frontmatter meta (untouched pre-072 rows)', () => {
    const meta = 'composed_by: 01ABCPARENT\nworktree: /tmp/wt'
    expect(readMetaList(meta, 'composed_by')).toEqual(['01ABCPARENT'])
  })

  it('reads a comma-separated frontmatter list', () => {
    const meta = 'composes: 01CHILDA, 01CHILDB'
    expect(readMetaList(meta, 'composes')).toEqual(['01CHILDA', '01CHILDB'])
  })

  it('returns [] for a missing key and for empty/undefined meta', () => {
    expect(readMetaList('{"composes":["x"]}', 'composed_by')).toEqual([])
    expect(readMetaList('', 'composed_by')).toEqual([])
    expect(readMetaList(undefined, 'composed_by')).toEqual([])
  })

  it('drops empty + non-string array members from JSON', () => {
    const meta = '{"composes":["01CHILDA","",123,null]}'
    expect(readMetaList(meta, 'composes')).toEqual(['01CHILDA'])
  })

  it('returns [] (not a throw) for malformed JSON-ish meta', () => {
    expect(readMetaList('{not valid json', 'composes')).toEqual([])
  })
})

describe('parseWorkContext — dual-read JSON + legacy frontmatter', () => {
  it('extracts work-context fields from JSON meta', () => {
    const meta = '{"branch":"feat/x","pr":"42","worktree":"/tmp/wt"}'
    expect(parseWorkContext(meta)).toEqual({
      branch: 'feat/x',
      pr: '42',
      worktree: '/tmp/wt',
    })
  })

  it('extracts work-context fields from legacy frontmatter meta', () => {
    const meta = 'branch: feat/x\npr: 42\nworktree: /tmp/wt'
    expect(parseWorkContext(meta)).toEqual({
      branch: 'feat/x',
      pr: '42',
      worktree: '/tmp/wt',
    })
  })

  it('ignores composition keys it does not own', () => {
    const meta = '{"composed_by":"01ABCPARENT","branch":"feat/x"}'
    expect(parseWorkContext(meta)).toEqual({ branch: 'feat/x' })
  })
})
