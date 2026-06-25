import { useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { AuditFacetRail, type FacetOption } from '@/components/audit/AuditFacetRail'
import { AuditAlertsRail } from '@/components/audit/AuditAlertsRail'
import { SavedSearches } from '@/pages/audit/SavedSearches'
import { useAuditAlerts } from '@/hooks/use-audit-alerts'
import type { AuditCapabilities, AuditFilter } from '@/api/types'
import { cn } from '@/lib/utils'

/**
 * AuditLeftRail — the Mission Control filter column. Collapsible Alerts section
 * (gated on capabilities), then optional Saved searches, then the faceted
 * filter rail. Stateless apart from the alerts collapse + its own alerts poll;
 * the page owns the filter and option lists.
 */
export function AuditLeftRail({
  filter,
  capabilities,
  workspaces,
  servers,
  routes,
  onFilterPatch,
  onApplySaved,
}: {
  filter: AuditFilter
  capabilities: AuditCapabilities
  workspaces: FacetOption[]
  servers: FacetOption[]
  routes: FacetOption[]
  onFilterPatch: (patch: Partial<AuditFilter>) => void
  onApplySaved: (q: string, f: AuditFilter) => void
}) {
  const [alertsOpen, setAlertsOpen] = useState(true)
  const { alerts, loading: alertsLoading } = useAuditAlerts({
    workspace_id: filter.workspace_id,
    enabled: capabilities.alerts,
  })

  return (
    <div className="space-y-5">
      {capabilities.alerts && (
        <div>
          <button
            type="button"
            onClick={() => setAlertsOpen((v) => !v)}
            className="flex w-full items-center justify-between text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70"
          >
            <span>Alerts{alerts.length > 0 ? ` (${alerts.length})` : ''}</span>
            <ChevronDown className={cn('h-3 w-3 transition-transform', !alertsOpen && '-rotate-90')} />
          </button>
          {alertsOpen && (
            <AuditAlertsRail
              alerts={alerts}
              loading={alertsLoading}
              onApplyFilter={(f: AuditFilter) => onFilterPatch(f)}
              className="mt-2"
            />
          )}
        </div>
      )}
      {capabilities.saved_searches && (
        <SavedSearches
          currentQuery={filter.q ?? ''}
          currentFilter={filter}
          workspaceId={filter.workspace_id}
          onApply={onApplySaved}
        />
      )}
      <AuditFacetRail
        filter={filter}
        onChange={onFilterPatch}
        workspaces={workspaces}
        servers={servers}
        routes={routes}
      />
    </div>
  )
}
