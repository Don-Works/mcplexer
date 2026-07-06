import type { ReactNode } from 'react'
import { Brain } from 'lucide-react'
import { getBrainStatus } from '@/api/brain'
import { useApi } from '@/hooks/use-api'

// Module-level so the fetcher keeps a stable identity — useApi refetches
// whenever the fetcher reference changes.
const fetchBrainStatus = (signal: AbortSignal) => getBrainStatus({ signal })

export function BrainGate({ children }: { children: ReactNode }) {
  const { data: status, loading, error } = useApi(fetchBrainStatus)

  if (loading) {
    return (
      <div className="flex items-center justify-center py-32">
        <div className="animate-pulse text-muted-foreground text-sm">Checking brain status...</div>
      </div>
    )
  }

  if (error || !status) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-32">
        <Brain className="h-12 w-12 text-muted-foreground/30" />
        <p className="max-w-md text-center text-sm text-muted-foreground">
          Could not reach the brain service.
        </p>
      </div>
    )
  }

  if (!status.enabled) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-32">
        <Brain className="h-12 w-12 text-muted-foreground/30" />
        <h2 className="text-lg font-semibold">Brain is not enabled</h2>
        <p className="max-w-md text-center text-sm text-muted-foreground">
          Enable it in settings to use this workspace.
        </p>
      </div>
    )
  }

  return <>{children}</>
}
