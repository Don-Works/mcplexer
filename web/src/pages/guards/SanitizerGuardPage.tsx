import { useCallback, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import { getSanitizerGuardDetail, updateSanitizerGuard } from '@/api/client'
import { ArrowLeft, Filter, Loader2 } from 'lucide-react'
import { toast } from 'sonner'

export function SanitizerGuardPage() {
  const fetcher = useCallback(() => getSanitizerGuardDetail(), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  const [saving, setSaving] = useState(false)

  async function toggleEnvelope(next: boolean) {
    setSaving(true)
    try {
      await updateSanitizerGuard(next)
      toast.success(next ? 'Envelope-always enabled' : 'Envelope-always disabled')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Update failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-5 max-w-5xl">
      <Link
        to="/guards"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Guards
      </Link>
      <div>
        <h1 className="text-2xl font-bold flex items-center gap-2">
          <Filter className="h-6 w-6" /> Sanitizer Guard
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Scans tool results for prompt-injection markers (denylist) and wraps
          untrusted content in &lt;tool_result&gt; envelopes so downstream models
          treat it as data, not instructions.
        </p>
      </div>

      {loading && !data ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading sanitizer…
        </div>
      ) : error ? (
        <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
          {error}
        </div>
      ) : data ? (
        <>
          <Card>
            <CardContent className="flex items-center justify-between gap-4 p-4">
              <div>
                <div className="text-sm font-medium">Always envelope</div>
                <div className="text-xs text-muted-foreground">
                  Wrap every tool result regardless of match. Strongest setting; costs a few extra tokens per call.
                </div>
                <div className="mt-1 text-[10px] text-muted-foreground/70">
                  Persisted in settings — survives daemon restart; takes effect on the next tool result.
                </div>
              </div>
              <Button
                variant={data.envelope_always ? 'default' : 'outline'}
                disabled={saving}
                onClick={() => toggleEnvelope(!data.envelope_always)}
                data-testid="sanitizer-envelope-toggle"
              >
                {saving && <Loader2 className="mr-2 h-3 w-3 animate-spin" />}
                {data.envelope_always ? 'On' : 'Off'}
              </Button>
            </CardContent>
          </Card>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Denylist rules
            </h2>
            <Card>
              <CardContent className="p-4">
                {data.denylist.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    No rules loaded.
                  </p>
                ) : (
                  <div className="flex flex-wrap gap-1.5">
                    {data.denylist.map((name) => (
                      <Badge key={name} variant="secondary" className="font-mono text-[10px]">
                        {name}
                      </Badge>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </section>

          <section className="space-y-2">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              Recent injection events
            </h2>
            <Card>
              <CardContent className="p-6 text-center text-sm text-muted-foreground">
                {data.recent_events.length === 0
                  ? 'No injection events recorded. New matches will appear here.'
                  : `${data.recent_events.length} events`}
              </CardContent>
            </Card>
          </section>
        </>
      ) : null}
    </div>
  )
}
