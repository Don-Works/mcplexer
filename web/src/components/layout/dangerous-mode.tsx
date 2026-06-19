// Dangerous-mode state, toggle, and chrome wash.
//
// Single source of truth for the global "disable every approval gate"
// switch. Lives in a tiny context so the header toggle, the under-header
// banner, and the AppShell viewport border all read from the same place
// without prop-drilling through every page.
//
// Persistence is server-side via PUT /api/v1/settings. We seed from
// GET on mount; the dashboard is the only writer so a single round trip
// per session is fine. Optimistic update + rollback on failure so the
// switch feels instant on flaky networks.
//
// Design intent (Vercel/Linear destructive-action grammar):
//  - OFF state: muted, single-line pill. Reads "Dangerous mode: off".
//  - ON state: vibrant red accent on the pill + a 1.5px pulsing border
//    around the viewport + a persistent banner under the header.
//    The pulse is gated through `motion-reduce:animate-none` so
//    accessibility prefs are respected.
//  - First turn-on triggers a confirm dialog. Turning off is one click.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { AlertTriangle, ShieldOff } from 'lucide-react'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { getSettings, updateSettings } from '@/api/client'
import type { Settings } from '@/api/types'
import { cn } from '@/lib/utils'

interface DangerousModeContextValue {
  enabled: boolean
  // setEnabled is the gated setter — turning ON triggers the confirm
  // dialog, turning OFF goes through immediately. Returns the resolved
  // boolean so callers can await the final state if they care.
  setEnabled: (next: boolean) => Promise<boolean>
  loading: boolean
}

const DangerousModeContext = createContext<DangerousModeContextValue | null>(
  null,
)

// useDangerousMode is the consumer-side hook. Throws if used outside the
// provider so we catch wiring mistakes loudly instead of silently
// rendering "off" everywhere.
export function useDangerousMode(): DangerousModeContextValue {
  const ctx = useContext(DangerousModeContext)
  if (!ctx) {
    throw new Error(
      'useDangerousMode must be used inside <DangerousModeProvider>',
    )
  }
  return ctx
}

interface DangerousModeProviderProps {
  children: ReactNode
}

export function DangerousModeProvider({ children }: DangerousModeProviderProps) {
  const [enabled, setEnabledState] = useState(false)
  const [loading, setLoading] = useState(true)
  const [confirmOpen, setConfirmOpen] = useState(false)
  // pendingResolve holds the promise resolver for an in-flight enable
  // request that's waiting on the modal. The modal calls either
  // onConfirm (true) or onCancel (false); both resolve the same promise.
  const [pendingResolve, setPendingResolve] = useState<
    ((confirmed: boolean) => void) | null
  >(null)

  // Bootstrap: read the server-persisted flag once on mount.
  useEffect(() => {
    let active = true
    getSettings()
      .then((res) => {
        if (active) setEnabledState(Boolean(res.settings.dangerous_mode_enabled))
      })
      .catch(() => {
        // Soft fail: stay false (safe default). A noisy toast on first
        // load is worse than silent fallback — the user can always
        // refresh.
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [])

  // persist optimistically: flip locally, fire the PUT, roll back on
  // failure with a clear toast. The server is the source of truth so we
  // re-seed from the response.
  const persist = useCallback(
    async (next: boolean) => {
      const prev = enabled
      setEnabledState(next)
      try {
        // We only send the fields we own — the server merges with the
        // full settings row, so we round-trip Load → mutate → Save to
        // avoid clobbering display_name etc. that the user might have
        // edited on the Settings page concurrently.
        const current = await getSettings()
        const updated: Settings = {
          ...current.settings,
          dangerous_mode_enabled: next,
        }
        const saved = await updateSettings(updated)
        setEnabledState(Boolean(saved.settings.dangerous_mode_enabled))
        toast.success(
          next
            ? 'Dangerous mode ON — approvals bypassed'
            : 'Dangerous mode OFF — approvals restored',
        )
      } catch (err) {
        setEnabledState(prev)
        const msg = err instanceof Error ? err.message : 'Failed to save'
        toast.error(`Could not toggle dangerous mode: ${msg}`)
      }
    },
    [enabled],
  )

  const setEnabled = useCallback(
    async (next: boolean): Promise<boolean> => {
      if (next === enabled) return enabled
      // Turning OFF is one-click, no confirm — restoring safety should
      // never have friction.
      if (!next) {
        await persist(false)
        return false
      }
      // Turning ON: open the modal and wait for the user's decision.
      return new Promise<boolean>((resolve) => {
        setPendingResolve(() => resolve)
        setConfirmOpen(true)
      })
    },
    [enabled, persist],
  )

  const handleConfirm = useCallback(async () => {
    setConfirmOpen(false)
    await persist(true)
    pendingResolve?.(true)
    setPendingResolve(null)
  }, [persist, pendingResolve])

  const handleCancel = useCallback(
    (open: boolean) => {
      if (open) return
      setConfirmOpen(false)
      pendingResolve?.(false)
      setPendingResolve(null)
    },
    [pendingResolve],
  )

  const value = useMemo<DangerousModeContextValue>(
    () => ({ enabled, setEnabled, loading }),
    [enabled, setEnabled, loading],
  )

  return (
    <DangerousModeContext.Provider value={value}>
      {children}
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={handleCancel}
        title="Enable dangerous mode?"
        description={
          'This disables every approval gate in the gateway. Shell-guard ' +
          'cheap-blocks, tool-call approvals, and policy resolvers will all ' +
          'be bypassed for as long as the toggle stays on. You may damage ' +
          'data, leak credentials, or run commands you would normally be ' +
          'stopped from. The audit trail keeps recording so you can review ' +
          'what was waved through, and the mcplexer data-dir lockdown ' +
          '(database, secrets, keys) stays enforced even in dangerous mode. ' +
          'Continue?'
        }
        confirmLabel="Enable dangerous mode"
        cancelLabel="Keep approvals on"
        variant="destructive"
        onConfirm={handleConfirm}
      />
    </DangerousModeContext.Provider>
  )
}

// DangerousModeToggle is the always-visible pill in the app header.
// Rendered with `data-testid="dangerous-mode-toggle"` for the e2e tests.
// Tasteful destructive styling: muted slate when off, saturated red when
// on. The ring on focus stays on the same accent so keyboard navigation
// can't accidentally make the on-state look broken.
export function DangerousModeToggle() {
  const { enabled, setEnabled, loading } = useDangerousMode()

  return (
    <button
      type="button"
      role="switch"
      aria-checked={enabled}
      aria-label={
        enabled
          ? 'Dangerous mode is on. Click to disable.'
          : 'Dangerous mode is off. Click to enable.'
      }
      data-testid="dangerous-mode-toggle"
      data-state={enabled ? 'on' : 'off'}
      disabled={loading}
      onClick={() => {
        void setEnabled(!enabled)
      }}
      className={cn(
        'group inline-flex h-8 shrink-0 items-center gap-2 rounded-full border px-2 text-[11px] font-medium tracking-wide transition-colors sm:px-3',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        enabled
          ? // ON: vibrant red, no doubt about what's happening.
            'border-red-500/70 bg-red-500/15 text-red-200 hover:bg-red-500/25 focus-visible:ring-red-500/70 shadow-[0_0_0_1px_rgba(239,68,68,0.25),0_0_18px_-4px_rgba(239,68,68,0.55)]'
          : // OFF: neutral, muted. Reads as a system control, not a CTA.
            'border-border bg-card/40 text-muted-foreground hover:border-border/80 hover:bg-card hover:text-foreground focus-visible:ring-border',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'inline-flex h-4 w-4 items-center justify-center transition-colors',
          enabled ? 'text-red-300' : 'text-muted-foreground/70',
        )}
      >
        {enabled ? (
          <AlertTriangle className="h-3.5 w-3.5" />
        ) : (
          <ShieldOff className="h-3.5 w-3.5" />
        )}
      </span>
      <span className="hidden whitespace-nowrap sm:inline">
        Dangerous mode: <span className={enabled ? 'font-semibold' : ''}>{enabled ? 'on' : 'off'}</span>
      </span>
      {/* Pip — a small dot that pulses while ON to signal "live". Sized
          conservatively; goes static under prefers-reduced-motion. */}
      <span
        aria-hidden
        className={cn(
          'inline-block h-1.5 w-1.5 rounded-full transition-colors',
          enabled
            ? 'bg-red-400 motion-safe:animate-pulse'
            : 'bg-muted-foreground/40',
        )}
      />
    </button>
  )
}

// DangerousModeBanner is the persistent under-header strip that warns
// the user the gates are off. Renders nothing when the mode is off so
// the layout doesn't shift on toggle. Keep copy precise — no all-caps,
// no exclamation points; the visual treatment carries the urgency.
export function DangerousModeBanner() {
  const { enabled } = useDangerousMode()
  if (!enabled) return null
  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="dangerous-mode-banner"
      className={cn(
        'flex shrink-0 items-center gap-2.5 border-b border-red-500/40 bg-red-500/10 px-4 py-1.5 text-[12px] text-red-200',
        // Subtle inner glow without animation — pulsing the whole strip
        // would be exhausting to look at over a long session.
        'shadow-[inset_0_-1px_0_0_rgba(239,68,68,0.25)]',
      )}
    >
      <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-red-300" aria-hidden />
      <span className="truncate">
        <span className="font-medium">Dangerous mode active</span>
        <span className="text-red-200/70"> — every approval gate is bypassed. Audit still recording.</span>
      </span>
    </div>
  )
}

// DangerousModeViewportFrame paints the 1.5px pulsing red border around
// the whole viewport when on. Rendered as a fixed, pointer-events-none
// overlay so it never intercepts clicks. Skipped entirely while off so
// we don't pay the layout cost.
export function DangerousModeViewportFrame() {
  const { enabled } = useDangerousMode()
  if (!enabled) return null
  return (
    <div
      aria-hidden
      data-testid="dangerous-mode-frame"
      className={cn(
        'pointer-events-none fixed inset-0 z-[60]',
        // Inner ring + soft outer glow. Box-shadow rather than border
        // so we don't push 1.5px of layout into every page.
        'shadow-[inset_0_0_0_1.5px_rgba(239,68,68,0.55),inset_0_0_24px_-6px_rgba(239,68,68,0.45)]',
        'motion-safe:animate-[pulse_2.6s_ease-in-out_infinite]',
      )}
    />
  )
}
