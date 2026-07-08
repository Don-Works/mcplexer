// TemplatesSection — the distilled view of everything the collectors
// saw: one row per masked line shape, ordered by lifetime volume.
// NEW + error-class is what wakes the worker; ack retires noisy
// shapes from novelty without hiding them from digests.
import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { toast } from 'sonner'
import { Check } from 'lucide-react'
import { ackTemplate } from '@/api/monitoring'
import type { MonitoringTemplate, Severity } from '@/api/monitoring'

const sevTone: Record<Severity, 'muted' | 'warn' | 'high' | 'critical'> = {
  info: 'muted', warn: 'warn', error: 'high', critical: 'critical',
}

export function TemplatesSection({ templates, refetch }: {
  templates: MonitoringTemplate[]
  refetch: () => void
}) {
  const [sevFilter, setSevFilter] = useState<Severity | 'all'>('all')
  const rows = templates.filter(t => sevFilter === 'all' || t.severity === sevFilter)

  return (
    <section>
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
          Templates <span className="normal-case tracking-normal">(24h window)</span>
        </h2>
        <div className="flex gap-1">
          {(['all', 'info', 'warn', 'error', 'critical'] as const).map(s => (
            <button key={s}
              className={
                'border px-1.5 py-0.5 text-xs ' +
                (sevFilter === s
                  ? 'border-primary/60 bg-accent text-foreground'
                  : 'border-border text-muted-foreground hover:bg-muted')
              }
              onClick={() => setSevFilter(s)}>
              {s}
            </button>
          ))}
        </div>
      </div>

      {rows.length === 0 ? (
        <p className="mt-2 text-sm text-muted-foreground">
          Nothing distilled in this window{sevFilter !== 'all' ? ` at ${sevFilter}` : ''}.
          Quiet logs are the good outcome.
        </p>
      ) : (
        <div className="mt-2 divide-y divide-border border border-border">
          {rows.map(t => (
            <div key={t.id} className="px-3 py-2 text-sm">
              <div className="flex items-center gap-2">
                {t.new && <Badge tone="info">new</Badge>}
                <Badge tone={sevTone[t.severity]}>{t.severity}</Badge>
                <span className="font-mono text-xs">×{t.window_lines || t.count}</span>
                <span className="flex-1 truncate font-mono text-xs" title={t.masked}>
                  {t.masked}
                </span>
                <span className="text-[10px] text-muted-foreground">
                  {t.source_name} · last {new Date(t.last_seen).toLocaleTimeString()}
                </span>
                {t.acked ? (
                  <Badge tone="muted" title={t.ack_note || 'acknowledged'}>acked</Badge>
                ) : (
                  <Button size="sm" variant="ghost"
                    title="mark known/expected — stops novelty wake-ups, stays in digests"
                    onClick={async () => {
                      await ackTemplate(t.id, 'acked from Monitoring page')
                      toast.success('template acked')
                      refetch()
                    }}>
                    <Check className="h-4 w-4" />
                  </Button>
                )}
              </div>
              <p className="mt-1 truncate pl-1 text-xs text-muted-foreground" title={t.sample_last}>
                {t.sample_last}
              </p>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
