import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { OpenRouterHarness, OpenRouterUsage } from '@/api/usage'

export function OpenRouterSection({ openrouter }: { openrouter: OpenRouterUsage }) {
  return (
    <div className="space-y-4">
      <OpenRouterCreditsSummary openrouter={openrouter} />
      {openrouter.by_harness.length > 0 && (
        <OpenRouterHarnessTable harnesses={openrouter.by_harness} />
      )}
    </div>
  )
}

function OpenRouterCreditsSummary({ openrouter }: { openrouter: OpenRouterUsage }) {
  const { credits, status, stale, error } = openrouter
  const hasCredits = Object.values(credits).some((value) => value != null)

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm font-medium">OpenRouter</CardTitle>
          <div className="flex items-center gap-2">
            <Badge variant={statusVariant(status)} className="text-xs">
              {statusLabel(status)}
            </Badge>
            {stale && <span className="text-xs text-amber-500">Stale</span>}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {error && (
          <div className="mb-3 text-xs text-destructive">{error}</div>
        )}
        {!hasCredits && (
          <p className="text-sm text-muted-foreground">
            No account credit data. Configure an OpenRouter auth-scope reference to add live limits; local harness activity still appears below.
          </p>
        )}
        {hasCredits && <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          {credits.usage != null && (
            <CreditStat label="Total spent" value={`$${credits.usage.toFixed(2)}`} />
          )}
          {credits.limit != null && (
            <CreditStat label="Limit" value={`$${credits.limit.toFixed(2)}`} />
          )}
          {credits.remaining != null && (
            <CreditStat label="Remaining" value={`$${credits.remaining.toFixed(2)}`} />
          )}
          {credits.usage_monthly != null && (
            <CreditStat label="Monthly" value={`$${credits.usage_monthly.toFixed(2)}`} />
          )}
          {credits.usage_weekly != null && (
            <CreditStat label="Weekly" value={`$${credits.usage_weekly.toFixed(2)}`} />
          )}
          {credits.usage_daily != null && (
            <CreditStat label="Daily" value={`$${credits.usage_daily.toFixed(2)}`} />
          )}
        </div>}
      </CardContent>
    </Card>
  )
}

function CreditStat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="font-mono text-lg font-medium">{value}</div>
    </div>
  )
}

function OpenRouterHarnessTable({ harnesses }: { harnesses: OpenRouterHarness[] }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-sm font-medium">OpenRouter by harness</CardTitle>
      </CardHeader>
      <CardContent className="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Harness</TableHead>
              <TableHead className="text-right">Requests</TableHead>
              <TableHead className="text-right">Tokens</TableHead>
              <TableHead className="text-right">Cost</TableHead>
              <TableHead>Models</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {harnesses.map((h) => (
              <HarnessRow key={h.harness} harness={h} />
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

function HarnessRow({ harness: h }: { harness: OpenRouterHarness }) {
  const totalTokens = h.input_tokens + h.output_tokens
  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{h.harness}</div>
        {h.accounting_missing_runs > 0 && (
          <div className="text-xs text-amber-500">
            {formatNumber(h.accounting_missing_runs)} runs unmeasured
          </div>
        )}
      </TableCell>
      <TableCell className="text-right font-mono text-sm">{formatNumber(h.requests)}</TableCell>
      <TableCell className="text-right font-mono text-sm">{formatTokens(totalTokens)}</TableCell>
      <TableCell className="text-right font-mono text-sm">
        {h.cost_kind === 'estimate' ? 'Est. ' : ''}${h.cost_usd.toFixed(2)}
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap gap-1">
          {h.models.slice(0, 3).map((m) => (
            <Badge key={m.model} variant="secondary" className="text-xs font-mono">
              {m.model.split('/').pop()}
            </Badge>
          ))}
          {h.models.length > 3 && (
            <Badge variant="outline" className="text-xs">+{h.models.length - 3}</Badge>
          )}
        </div>
      </TableCell>
    </TableRow>
  )
}

function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'ok': return 'default'
    case 'partial': return 'secondary'
    case 'unconfigured': return 'outline'
    case 'unavailable': return 'outline'
    case 'error': return 'destructive'
    default: return 'outline'
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case 'ok': return 'Available'
    case 'partial': return 'Partial'
    case 'unconfigured': return 'Unmeasured'
    case 'unavailable': return 'Not connected'
    case 'error': return 'Error'
    default: return status
  }
}

function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}
