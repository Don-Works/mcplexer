import type { AssistGuidanceNudge, AssistMemoryCandidate } from '@/api/brainBrowser'

// pulseArbiter enforces the one-pulse-per-record law (DESIGN §3.5/§4.4): on a
// single record, a guidance nudge and a memory candidate must NEVER pulse at
// the same time — the higher-signal affordance wins. This keeps the pulse
// vocabulary meaningful across the whole dashboard (a pulse always means one
// specific "waiting on you" thing, never ambient noise).
//
// Pure + table-tested so the priority order is auditable. The arbiter decides
// WHICH surface owns the pulse this render; the loser still renders, but
// without its pulse marker (a guidance nudge is still actionable, a candidate
// still listed — they simply don't compete for the eye).

// Signal ranks. A memory candidate that crossed the high bar
// (decision-with-rationale) is the strongest pulse — it is durable knowledge
// the operator is about to lose. Guidance nudges rank below it, ordered by how
// load-bearing the fix is: a record the agent can't act on well
// (missing-criteria) outranks cosmetic enrichment (auto-tag).
const GUIDANCE_RANK: Record<string, number> = {
  'missing-acceptance-criteria': 70,
  'link-related-memory': 60,
  'entity-extraction': 50,
  'auto-tag': 40,
}

const MEMORY_CANDIDATE_RANK = 100

export type PulseOwner = 'memory' | 'guidance' | 'none'

export interface PulseDecision {
  // owner names which surface gets the pulse this render.
  owner: PulseOwner
  // nudge is the single highest-signal guidance nudge to surface (the GUI
  // renders at most one inline nudge at a time). null when there are none.
  nudge: AssistGuidanceNudge | null
}

// guidanceRank returns the priority of a nudge kind (unknown kinds rank lowest
// but still above nothing).
export function guidanceRank(kind: string): number {
  return GUIDANCE_RANK[kind] ?? 10
}

// topNudge returns the single highest-ranked guidance nudge, or null.
export function topNudge(nudges: AssistGuidanceNudge[]): AssistGuidanceNudge | null {
  let best: AssistGuidanceNudge | null = null
  let bestRank = -1
  for (const n of nudges) {
    const r = guidanceRank(n.kind)
    if (r > bestRank) {
      best = n
      bestRank = r
    }
  }
  return best
}

// arbitratePulse decides the single pulse owner for a record given the live
// candidates + nudges. A memory candidate (high-bar) always outranks any
// guidance nudge; among nudges the highest-ranked kind wins. The losing
// surface still renders (the nudge is returned regardless of owner so the GUI
// can show it without a pulse), it just doesn't own the pulse.
export function arbitratePulse(
  candidates: AssistMemoryCandidate[],
  nudges: AssistGuidanceNudge[],
): PulseDecision {
  const nudge = topNudge(nudges)
  const hasCandidate = candidates.length > 0
  if (!hasCandidate && !nudge) return { owner: 'none', nudge: null }
  if (!nudge) return { owner: 'memory', nudge: null }
  if (!hasCandidate) return { owner: 'guidance', nudge }
  // Both present: compare the strongest of each.
  const owner = MEMORY_CANDIDATE_RANK >= guidanceRank(nudge.kind) ? 'memory' : 'guidance'
  return { owner, nudge }
}
