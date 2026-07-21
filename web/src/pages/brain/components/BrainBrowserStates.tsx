import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { ScopePickerSkeleton, RecordListSkeleton } from './RecordSkeleton'

// BrainBrowserStates holds the Ledger Console's non-data states (loading
// skeleton, error, empty index) so the page shell stays focused on data wiring
// and under the 300-line cap.

// BrainLoadingSkeleton is the geometry-matched shell skeleton (DESIGN §3.8):
// the scope-picker strip + a divide-y list skeleton, never a spinner.
export function BrainLoadingSkeleton() {
  return (
    <Card className="overflow-hidden rounded-none">
      <ScopePickerSkeleton />
      <div className="grid grid-cols-[minmax(320px,420px)_1fr]">
        <div className="min-h-[60vh] border-r border-border">
          <RecordListSkeleton />
        </div>
        <div className="p-4" />
      </div>
    </Card>
  )
}

// BrainErrorCard renders the read error, translating the brain-disabled case
// into the actionable enable hint.
export function BrainErrorCard({ error }: { error: string }) {
  const disabled = error.includes('not enabled') || error.includes('503')
  return (
    <Card className="rounded-none">
      <CardContent className="py-8 text-center text-sm text-muted-foreground">
        {disabled
          ? 'The Brain is disabled. Enable MCPLEXER_BRAIN_ENABLED and run brain_init + brain_import first.'
          : `Error: ${error}`}
      </CardContent>
    </Card>
  )
}

// BrainEmptyIndexCard is the no-workspaces state (distinct from a per-kind
// empty list — this means the index itself is empty).
export function BrainEmptyIndexCard() {
  return (
    <Card className="rounded-none">
      <CardContent className="py-8 text-center text-sm text-muted-foreground">
        <Badge tone="muted" className="mb-2">
          empty index
        </Badge>
        <div>No workspaces in the brain index yet. Run brain_init + brain_import first.</div>
      </CardContent>
    </Card>
  )
}
