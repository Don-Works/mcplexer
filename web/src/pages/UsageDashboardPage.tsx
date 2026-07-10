import { useCallback, useState } from 'react'
import { Gauge, Loader2, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/use-api'
import { toast } from 'sonner'
import { getUsage, refreshUsage, type UsageResponse } from '@/api/usage'
import { ProviderTable } from '@/pages/usage/ProviderTable'
import { OpenRouterSection } from '@/pages/usage/OpenRouterSection'
import { SkeletonPage } from '@/pages/usage/SkeletonPage'

export function UsageDashboardPage() {
  const fetcher = useCallback(() => getUsage(30), [])
  const { data, loading, error, refetch } = useApi(fetcher, 60_000)
  const [refreshing, setRefreshing] = useState(false)

  async function handleRefresh() {
    setRefreshing(true)
    try {
      await refreshUsage(30)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Usage refresh failed')
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        data={data}
        loading={loading}
        refreshing={refreshing}
        onRefresh={handleRefresh}
      />

      {error && (
        <div className="flex items-center justify-between gap-3 border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          <span>{error}</span>
          <Button size="sm" variant="ghost" onClick={refetch}>Retry</Button>
        </div>
      )}

      {loading && !data ? <SkeletonPage /> : null}

      {data && (
        <>
          <ProviderTable providers={data.providers} windowDays={data.window_days} />
          <OpenRouterSection openrouter={data.openrouter} />
        </>
      )}
    </div>
  )
}

function PageHeader({
  data,
  loading,
  refreshing,
  onRefresh,
}: {
  data: UsageResponse | null
  loading: boolean
  refreshing: boolean
  onRefresh: () => void
}) {
  return (
    <header className="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
          <Gauge className="h-6 w-6" /> AI usage
        </h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          Subscription allowances and observed consumption across providers.
        </p>
      </div>
      <div className="flex items-center gap-2">
        {data && (
          <span className="text-xs text-muted-foreground">
            {formatFreshness(data.generated_at)}
          </span>
        )}
        <Button
          variant="ghost"
          size="sm"
          onClick={onRefresh}
          disabled={loading || refreshing}
        >
          {loading || refreshing ? (
            <Loader2 className="mr-1.5 h-4 w-4 animate-spin" />
          ) : (
            <RefreshCw className="mr-1.5 h-4 w-4" />
          )}
          Refresh
        </Button>
      </div>
    </header>
  )
}

function formatFreshness(iso: string): string {
  try {
    const d = new Date(iso)
    const now = new Date()
    const diffMs = now.getTime() - d.getTime()
    const diffMin = Math.floor(diffMs / 60_000)
    if (diffMin < 1) return 'Just now'
    if (diffMin < 60) return `${diffMin}m ago`
    const diffH = Math.floor(diffMin / 60)
    if (diffH < 24) return `${diffH}h ago`
    return d.toLocaleDateString()
  } catch {
    return iso
  }
}
