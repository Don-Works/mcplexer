import type { BrainSearchHit } from '@/api/brainBrowser'

// indexTypeahead.ts — the pure ranking + option-assembly logic behind the
// shared IndexTypeahead dropdown (DESIGN §4.0/§4.1). The server already tiers
// + frecency-ranks the hits (internal/brain/editor_search.go); the client's
// job is to (a) keep that order stable, (b) cap the list, and (c) append the
// always-last create-on-miss row. Kept as plain functions so the ordering +
// create-on-miss decision are unit-testable without rendering a dropdown.

// TypeaheadMode is the one shared grammar used by cmd+K AND every in-field
// picker. The mono prefix the operator types selects the mode (DESIGN §4.0).
export type TypeaheadMode = 'ref' | 'tag' | 'workspace'

// TypeaheadOption is one rendered row: either a real index hit or the
// create-on-miss row (always last). The dropdown renders both identically
// except the create row carries `create: true`.
export interface TypeaheadOption {
  // id is a stable React key + the value committed on select. For a hit it is
  // the record id (ref) / tag text / workspace id; for create it is the typed
  // text being created.
  id: string
  // label is the human line (record title, "#tag", "@workspace").
  label: string
  // sub is the mono meta line ("task · review", "memory", "tag", "workspace").
  sub: string
  // tier mirrors the server tier (0 exact-prefix, 1 token, 2 fuzzy) for hits;
  // create rows have no tier.
  tier?: number
  // create marks the create-on-miss row.
  create?: boolean
  // hit is the underlying search hit for ref mode (so the caller can read kind).
  hit?: BrainSearchHit
}

// tierLabel renders the mono meta line for a ref hit: "<kind> · <status>".
function tierLabel(h: BrainSearchHit): string {
  const k = h.kind === 'task' ? 'task' : 'memory'
  return h.status ? `${k} · ${h.status}` : k
}

// rankRefOptions turns the server's ranked ref hits into rendered options. The
// server order (tier asc, frecency desc) is authoritative, so this only maps +
// caps. `query` seeds the create-on-miss row; an exact-title match suppresses
// the create row (you cannot create a duplicate of an exact hit) — referencing
// an existing record by its exact title should pick it, not fork a stub.
export function rankRefOptions(
  hits: BrainSearchHit[],
  query: string,
  limit = 8,
): TypeaheadOption[] {
  const q = query.trim()
  const opts: TypeaheadOption[] = hits.slice(0, limit).map((h) => ({
    id: h.id,
    label: h.title || h.id,
    sub: tierLabel(h),
    tier: h.tier,
    create: false,
    hit: h,
  }))
  if (q && !hasExactTitle(hits, q)) {
    opts.push({ id: q, label: `Create "${q}" as a new record`, sub: 'create', create: true })
  }
  return opts
}

// rankTagOptions turns ref-or-tag hits' tags into a deduped, prefix-ranked tag
// option list. Tags are not first-class index rows, so we mine them out of the
// returned hits' tag arrays and rank by prefix-then-substring against the
// query. The create-on-miss row appends a brand-new tag when no exact tag
// matches.
export function rankTagOptions(
  hits: BrainSearchHit[],
  query: string,
  limit = 8,
): TypeaheadOption[] {
  const q = query.trim().toLowerCase()
  const seen = new Set<string>()
  const ranked: { tag: string; rank: number }[] = []
  for (const h of hits) {
    for (const raw of h.tags ?? []) {
      const tag = raw.trim()
      const key = tag.toLowerCase()
      if (!tag || seen.has(key)) continue
      seen.add(key)
      const rank = q === '' ? 2 : key.startsWith(q) ? 0 : key.includes(q) ? 1 : -1
      if (rank < 0) continue
      ranked.push({ tag, rank })
    }
  }
  ranked.sort((a, b) => (a.rank !== b.rank ? a.rank - b.rank : a.tag.localeCompare(b.tag)))
  const opts: TypeaheadOption[] = ranked.slice(0, limit).map((r) => ({
    id: r.tag,
    label: `#${r.tag}`,
    sub: 'tag',
    tier: r.rank,
    create: false,
  }))
  const exact = ranked.some((r) => r.tag.toLowerCase() === q)
  if (q && !exact) {
    opts.push({ id: query.trim(), label: `Create #${query.trim()}`, sub: 'new tag', create: true })
  }
  return opts
}

// hasExactTitle reports whether any hit's title equals the query (case-insens).
function hasExactTitle(hits: BrainSearchHit[], query: string): boolean {
  const q = query.toLowerCase()
  return hits.some((h) => (h.title || '').toLowerCase() === q)
}

// detectMode parses the leading mono prefix of a cmd+K / in-field query into a
// mode + the residual text after the prefix (DESIGN §4.0 grammar table).
// Returns null for the no-prefix filter mode (the existing fuzzy-jump path).
export function detectMode(
  raw: string,
): { mode: TypeaheadMode | 'cmd'; text: string } | null {
  if (raw.startsWith('[[')) return { mode: 'ref', text: raw.slice(2) }
  if (raw.startsWith('#')) return { mode: 'tag', text: raw.slice(1) }
  if (raw.startsWith('@')) return { mode: 'workspace', text: raw.slice(1) }
  if (raw.startsWith('>')) return { mode: 'cmd', text: raw.slice(1).trimStart() }
  return null
}
