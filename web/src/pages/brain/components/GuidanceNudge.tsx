import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { AssistGuidanceNudge } from '@/api/brainBrowser'

// GuidanceNudge renders a single inline guidance suggestion (DESIGN §4.4) as a
// calm single-line warn-tone strip with a one-click apply — NEVER a popup,
// modal, or toast. It sits in-field under the body, in the same "waiting on
// you" register as the rest of the brain's ambient AI cadence.
//
// The one-pulse-per-record law (DESIGN §3.5/§4.4) is enforced by the parent:
// guidance and a memory candidate never pulse on the same record at once, and
// the higher-signal pulse wins. This component renders only the highest-signal
// nudge passed to it; the pulse marker is decorative + aria-hidden with a
// text-equivalent, and respects prefers-reduced-motion (a static marker via the
// motion-reduce utility) so colour/motion is never the sole signal.
export function GuidanceNudge({
  nudge,
  onApply,
  onDismiss,
}: {
  nudge: AssistGuidanceNudge
  onApply: (n: AssistGuidanceNudge) => void
  onDismiss: (n: AssistGuidanceNudge) => void
}) {
  return (
    <div
      role="status"
      aria-live="polite"
      className="animate-[audit-in_0.45s_ease-out] flex items-center gap-2 border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-xs text-amber-300"
    >
      {/* Warn glyph + waiting pulse: icon-and-text, not colour alone. The pulse
          is aria-hidden and reduces to a static marker under reduced-motion. */}
      <span className="relative inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center" aria-hidden>
        <span className="absolute inset-0 animate-pulse-slow bg-amber-400/30 motion-reduce:animate-none" />
        <AlertTriangle className="relative h-3.5 w-3.5" />
      </span>
      <span className="min-w-0 flex-1 truncate">{nudge.message}</span>
      <Button
        size="sm"
        variant="ghost"
        className="h-6 rounded-none px-2 text-amber-200 hover:bg-amber-500/20 hover:text-amber-100"
        onClick={() => onApply(nudge)}
      >
        apply
      </Button>
      <Button
        size="sm"
        variant="ghost"
        className="h-6 rounded-none px-2 text-muted-foreground"
        onClick={() => onDismiss(nudge)}
        aria-label="dismiss suggestion"
      >
        dismiss
      </Button>
    </div>
  )
}
