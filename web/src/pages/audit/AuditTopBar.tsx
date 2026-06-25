import { Pause, Play } from 'lucide-react'
import { AuditSearchBox } from '@/components/audit/AuditSearchBox'
import type { AuditCapabilities, AuditSearchMode } from '@/api/types'
import { cn } from '@/lib/utils'

/**
 * AuditTopBar — Mission Control center header: the search box plus the live
 * indicator with a pause/resume toggle and a "N new" pill when paused. The
 * emerald pulsing dot is preserved verbatim from the original page. Pure
 * presentation — all state lives in the parent.
 */
export function AuditTopBar({
  query,
  onQueryChange,
  onSearchSubmit,
  searchMode,
  capabilities,
  connected,
  paused,
  onTogglePause,
  bufferedCount,
  onFlush,
}: {
  query: string
  onQueryChange: (v: string) => void
  onSearchSubmit: (v: string) => void
  searchMode: AuditSearchMode | null
  capabilities: AuditCapabilities
  connected: boolean
  paused: boolean
  onTogglePause: () => void
  bufferedCount: number
  onFlush: () => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <AuditSearchBox
        value={query}
        onChange={onQueryChange}
        onSubmit={onSearchSubmit}
        mode={searchMode}
        capabilities={capabilities}
        className="min-w-[16rem] flex-1"
      />

      {paused && bufferedCount > 0 && (
        <button
          type="button"
          data-testid="audit-flush-buffered"
          onClick={onFlush}
          className="inline-flex shrink-0 items-center gap-1 border border-primary/40 bg-primary/5 px-2 py-1.5 font-mono text-xs tabular-nums text-primary transition-colors hover:bg-primary/10"
        >
          {bufferedCount} new
        </button>
      )}

      <div className="flex shrink-0 items-center gap-2 text-sm">
        {connected ? (
          <>
            <span className="relative flex h-2.5 w-2.5">
              {!paused && (
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
              )}
              <span
                className={cn(
                  'relative inline-flex h-2.5 w-2.5 rounded-full',
                  paused ? 'bg-muted-foreground/50' : 'bg-emerald-500',
                )}
              />
            </span>
            <span className={paused ? 'text-muted-foreground' : 'text-emerald-400'}>
              {paused ? 'Paused' : 'Live'}
            </span>
          </>
        ) : (
          <>
            <span className="h-2.5 w-2.5 rounded-full bg-muted-foreground/40" />
            <span className="text-muted-foreground">Connecting...</span>
          </>
        )}
        <button
          type="button"
          data-testid="audit-pause-toggle"
          aria-label={paused ? 'Resume live feed' : 'Pause live feed'}
          onClick={onTogglePause}
          className="inline-flex h-7 w-7 items-center justify-center border border-border text-muted-foreground transition-colors hover:border-border/80 hover:text-foreground"
        >
          {paused ? <Play className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
        </button>
      </div>
    </div>
  )
}
