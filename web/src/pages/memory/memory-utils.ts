// memory-utils — non-component helpers split out of memory-primitives.tsx
// so the eslint react-refresh rule (only-export-components) stays happy.

import type { MemoryEntry } from '@/api/memory'

// scopeOf returns 'workspace' | 'global' | 'peer'. Memories carrying a
// workspace_id are workspace-scoped; bare are global. Peer-shared (those
// carrying origin_peer_id or source_kind === "peer") get a "peer" tone.
export type MemoryScope = 'workspace' | 'global' | 'peer'
export function scopeOf(
  m: Pick<MemoryEntry, 'workspace_id' | 'origin_peer_id' | 'source_kind'>,
): MemoryScope {
  if (m.origin_peer_id || m.source_kind === 'peer') return 'peer'
  if (m.workspace_id) return 'workspace'
  return 'global'
}

// previewText returns the first non-blank chunk of memory content,
// capped at maxLen with an ellipsis. Keeps tables scannable.
export function previewText(content: string, maxLen = 120): string {
  // Strip optional yaml frontmatter the way the markdown helper does, so
  // notes that lead with --- don't render as opaque metadata.
  const stripped = content.replace(/^---\n[\s\S]*?\n---\n*/, '')
  const flat = stripped.replace(/\s+/g, ' ').trim()
  if (flat.length <= maxLen) return flat
  return flat.slice(0, maxLen - 1).trimEnd() + '…'
}

// parseTags accepts the wire-format tags field. The backend hands us
// MemoryEntry.tags as either string[] (preferred) or a JSON raw-message —
// the API serializer flattens to []. We also accept comma-separated for
// safety with potential future shape drift.
export function parseTags(tags: MemoryEntry['tags']): string[] {
  if (!tags) return []
  if (Array.isArray(tags)) return tags.filter(Boolean)
  if (typeof tags === 'string') {
    return (tags as string)
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
  }
  return []
}

// isMemoryEvent — tolerant matcher for the notification stream. Backend
// emits memory mutations via notify.Bus with source==='memory'; we also
// accept any memory_ / memory.* kind. Shared by the landing activity feed
// and the brain-stats live-refresh so both react to the same events.
export function isMemoryEvent(kind?: string, source?: string): boolean {
  if (source === 'memory') return true
  if (!kind) return false
  return (
    kind.startsWith('memory_') ||
    kind === 'memory.written' ||
    kind === 'memory.offered' ||
    kind === 'memory.consolidated'
  )
}

// relativeTime — mirror of the helper in AuditDetailDialog so memory
// pages get the same cadence without a cross-file import.
export function relativeTime(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return iso
  const diff = Date.now() - then
  const abs = Math.abs(diff)
  const sign = diff < 0 ? 'in ' : ''
  const suffix = diff < 0 ? '' : ' ago'
  const sec = Math.round(abs / 1000)
  if (sec < 5) return 'just now'
  if (sec < 60) return `${sign}${sec}s${suffix}`
  const min = Math.round(sec / 60)
  if (min < 60) return `${sign}${min}m${suffix}`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${sign}${hr}h${suffix}`
  const day = Math.round(hr / 24)
  if (day < 30) return `${sign}${day}d${suffix}`
  const mo = Math.round(day / 30)
  if (mo < 12) return `${sign}${mo}mo${suffix}`
  return `${sign}${Math.round(mo / 12)}y${suffix}`
}
