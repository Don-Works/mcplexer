// RunnerStrip — the peer-responsibilities panel. One glance answers:
// is THIS daemon the peer group's monitoring runner (collectors +
// log-watch workers execute here), or a viewer that receives alerts
// and reads digests while another daemon does the work?
import { Badge } from '@/components/ui/badge'
import { Radio, Eye } from 'lucide-react'
import type { MonitoringStatus } from '@/api/monitoring'

export function RunnerStrip({ status }: { status: MonitoringStatus | null }) {
  if (!status) return null
  const runner = status.runner_enabled
  return (
    <div className="flex items-center gap-3 border border-border bg-muted/40 px-4 py-3">
      {runner ? (
        <Radio className="h-4 w-4 text-primary shrink-0" />
      ) : (
        <Eye className="h-4 w-4 text-muted-foreground shrink-0" />
      )}
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <span className="font-mono text-sm">{status.gateway_hostname}</span>
          <Badge tone={runner ? 'success' : 'muted'}>
            {runner ? 'runner' : 'viewer'}
          </Badge>
        </div>
        <p className="text-xs text-muted-foreground">
          {runner
            ? 'This daemon executes monitoring jobs: it pulls remote logs on schedule and runs the log-watch worker. Exactly one daemon per peer group should hold this role.'
            : 'Viewer: jobs run on the peer group’s runner daemon. This daemon receives alerts over the mesh and can read digests, but never pulls logs or runs the worker (MCPLEXER_MONITORING_RUNNER=0).'}
        </p>
      </div>
    </div>
  )
}
