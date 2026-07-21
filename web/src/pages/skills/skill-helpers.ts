// skill-helpers.ts — small pure helpers shared between the skills page
// pieces. Keeping category + tag extraction in one place means the rules
// for "what counts as uncategorized" or "how do we dedupe tags" live in
// exactly one spot rather than three components disagreeing.

import type { SkillRegistryEntry } from '@/api/client'

// UNCATEGORIZED is the bucket every entry without a `category:` field
// falls into. Exported so consumers (filter chips, section headers) can
// reference it by name rather than the magic string.
export const UNCATEGORIZED = 'uncategorized'

// categoryOf extracts the parsed category from metadata.category, or
// returns UNCATEGORIZED. The parser already lower-cases + validates the
// shape (see internal/skillregistry/parse.go); we just defensively
// re-normalise so a malformed row never breaks the UI.
export function categoryOf(entry: SkillRegistryEntry): string {
  const raw = entry.metadata?.['category']
  if (typeof raw !== 'string') return UNCATEGORIZED
  const s = raw.trim().toLowerCase()
  return s || UNCATEGORIZED
}

// groupByCategory partitions entries by category, preserving the input
// order within each group. Returns groups in a stable sort: alphabetical
// by category, with UNCATEGORIZED always last regardless of letter.
export function groupByCategory(
  entries: SkillRegistryEntry[],
): { category: string; entries: SkillRegistryEntry[] }[] {
  const buckets = new Map<string, SkillRegistryEntry[]>()
  for (const e of entries) {
    const c = categoryOf(e)
    const bucket = buckets.get(c)
    if (bucket) {
      bucket.push(e)
    } else {
      buckets.set(c, [e])
    }
  }
  const out = Array.from(buckets.entries()).map(([category, entries]) => ({
    category,
    entries,
  }))
  out.sort((a, b) => {
    if (a.category === UNCATEGORIZED) return 1
    if (b.category === UNCATEGORIZED) return -1
    return a.category.localeCompare(b.category)
  })
  return out
}

// tagFrequency counts how many times each tag appears across entries,
// returning the [tag, count] pairs sorted by count desc then alpha.
// Used to populate the TagBar — most-common tags shown first.
export function tagFrequency(
  entries: SkillRegistryEntry[],
): { tag: string; count: number }[] {
  const counts = new Map<string, number>()
  for (const e of entries) {
    for (const t of e.tags ?? []) {
      const key = t.trim().toLowerCase()
      if (!key) continue
      counts.set(key, (counts.get(key) ?? 0) + 1)
    }
  }
  const out = Array.from(counts.entries()).map(([tag, count]) => ({ tag, count }))
  out.sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag))
  return out
}

// matchesTagFilter returns true when entry carries every selected tag.
// AND semantics — clicking two chips narrows the list. Case-insensitive
// to match tagFrequency's normalisation.
export function matchesTagFilter(
  entry: SkillRegistryEntry,
  selected: Set<string>,
): boolean {
  if (selected.size === 0) return true
  const have = new Set((entry.tags ?? []).map((t) => t.trim().toLowerCase()))
  for (const want of selected) {
    if (!have.has(want)) return false
  }
  return true
}
