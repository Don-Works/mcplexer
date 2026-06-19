import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// ModelPresenceLabel is the single standing "AI is here" chrome (DESIGN
// §3.4 / §4.2): a calm mono `model · <profile>` provenance anchor. It
// SHIMMERS only while a completion is in flight (animate-shimmer on the
// label, never the field, never a scattered spinner) and otherwise sits
// static. It announces the in-flight state via aria-live="polite" so a
// screen reader hears "suggestion loading" without the visual sheen.
//
// When no model profile is configured the label is absent entirely — the
// caller renders nothing (silent degrade). This component assumes a profile
// exists; pass profile={null} only to render the "no model" disabled hint
// when you explicitly want to show provenance is unconfigured.
export function ModelPresenceLabel({
  profile,
  inFlight,
}: {
  profile: string | null
  inFlight: boolean
}) {
  // Silent degrade: no profile => no chrome at all.
  if (!profile) return null

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className="relative inline-flex select-none items-center gap-1 px-1 py-0.5 font-mono text-[11px] text-muted-foreground"
          aria-live="polite"
        >
          {inFlight && (
            <span
              className="animate-shimmer pointer-events-none absolute inset-0"
              aria-hidden
            />
          )}
          <span className="relative">
            model · {profile}
          </span>
          {/* Text-only announcement of the in-flight state for AT (the
              shimmer is decorative + aria-hidden). */}
          {inFlight && <span className="sr-only">suggestion loading</span>}
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom" className="font-mono text-xs">
        {inFlight ? `${profile} · generating` : `model profile: ${profile}`}
      </TooltipContent>
    </Tooltip>
  )
}
