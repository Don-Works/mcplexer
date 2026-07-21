// Source glyphs — single ASCII char per notification source. Same
// energy as the `›` active marker in the sidebar and `mcplexer›` prompt
// in the command palette. Color carries priority (see priorityTone),
// glyph carries source.

export const SOURCE_GLYPH: Record<string, string> = {
  mesh: '~',
  approval: '?',
  system: '!',
  secret: '*',
}

export function glyphFor(kind: string): string {
  return SOURCE_GLYPH[kind] ?? '·'
}

// priorityTone — Tailwind class chunks for glyph color. Priority is the
// only thing colored; source is shape-only.
export function priorityGlyphTone(priority: string): string {
  switch (priority) {
    case 'critical':
      return 'text-destructive'
    case 'high':
      return 'text-amber-400'
    case 'low':
      return 'text-muted-foreground/60'
    default:
      return 'text-primary'
  }
}
