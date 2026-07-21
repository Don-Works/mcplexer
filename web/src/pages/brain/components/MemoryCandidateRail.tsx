import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ChevronRight } from 'lucide-react'
import type { AssistMemoryCandidate } from '@/api/brainBrowser'

// MemoryCandidateRail is the ambient proactive-memory inbox (DESIGN §3.5): a
// quiet right rail, never a popup. It surfaces at most ~2 candidates; the rest
// collapse to "+ N more". Each candidate slides in with the calm "waiting on
// you" cadence (animate-pulse-slow marker), exposes a `why?` reveal of the
// detected signal, and offers save / not now / never. It NEVER toasts,
// modals, or steals focus.
//
// Wiring: the parent fetches candidates at a session boundary (blur/save/idle)
// and passes them in; onSave writes the memory via the serializer; onNever
// records the sticky per-record suppression (content-hash). One pulse per
// record is enforced by the parent only fetching one record's candidates.

const VISIBLE_CAP = 2

export function MemoryCandidateRail({
  candidates,
  onSave,
  onDismiss,
  onNever,
  pulse = true,
}: {
  candidates: AssistMemoryCandidate[]
  onSave: (c: AssistMemoryCandidate) => void
  onDismiss: (c: AssistMemoryCandidate) => void
  onNever: (c: AssistMemoryCandidate) => void
  // pulse gates the "waiting on you" marker so the one-pulse-per-record law
  // (DESIGN §3.5/§4.4) holds: when a guidance nudge owns the pulse this render,
  // the rail still lists its candidates but suppresses its own pulse marker.
  pulse?: boolean
}) {
  const [expanded, setExpanded] = useState(false)
  if (candidates.length === 0) return null

  const visible = expanded ? candidates : candidates.slice(0, VISIBLE_CAP)
  const overflow = candidates.length - visible.length

  return (
    <aside className="w-full space-y-2" aria-label="Memory suggestions">
      {visible.map((c, i) => (
        <CandidateCard
          key={c.content_hash}
          candidate={c}
          // Only the first (top) card carries the pulse, and only when this
          // surface owns the record's single pulse this render.
          pulse={pulse && i === 0}
          onSave={() => onSave(c)}
          onDismiss={() => onDismiss(c)}
          onNever={() => onNever(c)}
        />
      ))}
      {!expanded && overflow > 0 && (
        <button
          type="button"
          className="font-mono text-xs text-muted-foreground hover:text-foreground"
          onClick={() => setExpanded(true)}
        >
          + {overflow} more
        </button>
      )}
    </aside>
  )
}

function CandidateCard({
  candidate,
  pulse,
  onSave,
  onDismiss,
  onNever,
}: {
  candidate: AssistMemoryCandidate
  pulse: boolean
  onSave: () => void
  onDismiss: () => void
  onNever: () => void
}) {
  const [showWhy, setShowWhy] = useState(false)
  return (
    <div className="animate-[audit-in_0.45s_ease-out] border border-border bg-card p-3">
      <div className="mb-2 flex items-center justify-between">
        <span className="text-xs text-muted-foreground">save as memory?</span>
        {/* "waiting on you" marker — pulse-slow, decorative + aria-hidden, gated
            by the one-pulse-per-record arbiter. Reduces to a static dot under
            prefers-reduced-motion so the meaning survives without animation. */}
        {pulse && (
          <>
            <span
              className="animate-pulse-slow inline-block h-2 w-2 bg-primary motion-reduce:animate-none"
              aria-hidden
            />
            <span className="sr-only">a memory suggestion is waiting for you</span>
          </>
        )}
      </div>

      <p className="mb-2 text-sm">{candidate.text}</p>

      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        <Badge tone="mono" className="text-[10px]">
          {candidate.kind}
        </Badge>
        {(candidate.tags ?? []).map((t) => (
          <span key={t} className="font-mono text-[11px] text-muted-foreground">
            #{t}
          </span>
        ))}
        {(candidate.refs ?? []).map((r) => (
          <span key={r} className="font-mono text-[11px] text-primary">
            [[{r}]]
          </span>
        ))}
      </div>

      <button
        type="button"
        className="mb-2 flex items-center gap-0.5 font-mono text-[11px] text-muted-foreground hover:text-foreground"
        onClick={() => setShowWhy((v) => !v)}
        aria-expanded={showWhy}
      >
        <ChevronRight
          className={`h-3 w-3 transition-transform ${showWhy ? 'rotate-90' : ''}`}
          aria-hidden
        />
        why?
      </button>
      {showWhy && (
        <div className="mb-2 border border-border bg-background p-2 font-mono text-[11px] text-muted-foreground">
          signal: {candidate.signal}
        </div>
      )}

      <div className="flex items-center gap-1.5">
        <Button size="sm" className="rounded-none" onClick={onSave}>
          save memory
        </Button>
        <Button size="sm" variant="ghost" className="rounded-none" onClick={onDismiss}>
          not now
        </Button>
        <Button size="sm" variant="ghost" className="rounded-none text-muted-foreground" onClick={onNever}>
          never
        </Button>
      </div>
    </div>
  )
}
