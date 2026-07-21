import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import {
  acceptDescription,
  listDescriptionVersions,
  rejectDescription,
} from '@/api/client'
import type { ToolDescriptionVersion } from '@/api/types'
import { Check, Clock, RotateCcw, X } from 'lucide-react'
import { toast } from 'sonner'
import { BuiltinDescriptionOverrides } from '@/components/BuiltinDescriptionOverrides'

function formatTime(ts: string): string {
  return new Date(ts).toLocaleString()
}

function statusBadge(status: string) {
  switch (status) {
    case 'active':
      return <Badge className="bg-emerald-500/10 text-emerald-400 border-emerald-500/30">active</Badge>
    case 'pending':
      return <Badge variant="secondary"><Clock className="h-3 w-3 mr-1" />pending</Badge>
    case 'rejected':
      return <Badge variant="destructive">rejected</Badge>
    case 'superseded':
      return <Badge variant="outline" className="text-muted-foreground">superseded</Badge>
    default:
      return <Badge variant="outline">{status}</Badge>
  }
}

function sourceBadge(source: string) {
  switch (source) {
    case 'model':
      return <Badge variant="outline" className="text-blue-400 border-blue-500/30">model</Badge>
    case 'manual':
      return <Badge variant="outline" className="text-amber-400 border-amber-500/30">manual</Badge>
    case 'original':
      return <Badge variant="outline" className="text-muted-foreground">original</Badge>
    default:
      return <Badge variant="outline">{source}</Badge>
  }
}

function PendingCard({
  version,
  onResolved,
}: {
  version: ToolDescriptionVersion
  onResolved: () => void
}) {
  const [reviewNote, setReviewNote] = useState('')
  const [resolving, setResolving] = useState(false)

  async function handleAccept() {
    setResolving(true)
    try {
      await acceptDescription(version.id, reviewNote)
      toast.success(`Accepted description for ${version.tool_name}`)
      onResolved()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to accept')
    } finally {
      setResolving(false)
    }
  }

  async function handleReject() {
    if (!reviewNote.trim()) {
      toast.error('A review note is required when rejecting')
      return
    }
    setResolving(true)
    try {
      await rejectDescription(version.id, reviewNote)
      toast.success(`Rejected suggestion for ${version.tool_name}`)
      onResolved()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to reject')
    } finally {
      setResolving(false)
    }
  }

  return (
    <Card className="border-primary/30">
      <CardContent className="pt-5 space-y-3">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1 min-w-0">
            <div className="font-mono text-sm text-accent-foreground break-all">
              {version.tool_name}
            </div>
            <div className="text-xs text-muted-foreground">
              Suggested by {version.model || 'unknown model'}
              {' '}&middot;{' '}{formatTime(version.created_at)}
            </div>
          </div>
          <div className="flex gap-1.5">{sourceBadge(version.source)}</div>
        </div>

        <div className="rounded-md bg-muted/50 p-3">
          <div className="text-xs font-medium text-muted-foreground mb-1">Proposed Description</div>
          <div className="text-sm whitespace-pre-wrap">{version.description}</div>
        </div>

        {version.rationale && (
          <div className="rounded-md bg-blue-500/5 border border-blue-500/20 p-3">
            <div className="text-xs font-medium text-blue-400 mb-1">Rationale</div>
            <div className="text-sm text-muted-foreground">{version.rationale}</div>
          </div>
        )}

        <div className="flex items-center gap-2 pt-1">
          <Input
            placeholder="Review note (required for rejection)"
            value={reviewNote}
            onChange={(e) => setReviewNote(e.target.value)}
            className="flex-1 h-8 text-sm"
            data-testid={`description-review-note-${version.id}`}
            aria-label="Review note"
          />
          <Button
            size="sm"
            onClick={handleAccept}
            disabled={resolving}
            className="gap-1.5"
            data-testid={`description-accept-${version.id}`}
          >
            <Check className="h-3.5 w-3.5" /> Accept
          </Button>
          <Button
            size="sm"
            variant="destructive"
            onClick={handleReject}
            disabled={resolving}
            className="gap-1.5"
            data-testid={`description-reject-${version.id}`}
          >
            <X className="h-3.5 w-3.5" /> Reject
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function HistoryTable({
  versions,
  onRollback,
}: {
  versions: ToolDescriptionVersion[]
  onRollback: () => void
}) {
  const [rolling, setRolling] = useState<string | null>(null)

  async function handleRollback(id: string) {
    setRolling(id)
    try {
      await acceptDescription(id, 'Rolled back from dashboard')
      toast.success('Rolled back successfully')
      onRollback()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to rollback')
    } finally {
      setRolling(null)
    }
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Tool</TableHead>
          <TableHead>Source</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Model</TableHead>
          <TableHead>Date</TableHead>
          <TableHead className="w-10" />
        </TableRow>
      </TableHeader>
      <TableBody>
        {versions.map((v) => (
          <TableRow key={v.id}>
            <TableCell className="font-mono text-xs">{v.tool_name}</TableCell>
            <TableCell>{sourceBadge(v.source)}</TableCell>
            <TableCell>{statusBadge(v.status)}</TableCell>
            <TableCell className="text-xs text-muted-foreground">{v.model || '-'}</TableCell>
            <TableCell className="text-xs text-muted-foreground">{formatTime(v.created_at)}</TableCell>
            <TableCell>
              {(v.status === 'superseded' || v.status === 'rejected') && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 gap-1 text-xs"
                  disabled={rolling === v.id}
                  data-testid={`description-restore-${v.id}`}
                  aria-label={`Restore description for ${v.tool_name}`}
                  onClick={() => handleRollback(v.id)}
                >
                  <RotateCcw className="h-3 w-3" /> Restore
                </Button>
              )}
            </TableCell>
          </TableRow>
        ))}
        {versions.length === 0 && (
          <TableRow>
            <TableCell colSpan={6} className="text-center text-muted-foreground py-8">
              No description versions yet
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  )
}

const VALID_STATUSES = ['', 'active', 'pending', 'superseded', 'rejected']

export function DescriptionsPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [statusFilter, setStatusFilter] = useState<string>(() => {
    const s = searchParams.get('status') ?? ''
    return VALID_STATUSES.includes(s) ? s : ''
  })

  useEffect(() => {
    const next = new URLSearchParams(searchParams)
    if (statusFilter) next.set('status', statusFilter)
    else next.delete('status')
    if (next.toString() !== searchParams.toString()) {
      setSearchParams(next, { replace: true })
    }
  }, [statusFilter, searchParams, setSearchParams])

  const fetchPending = useCallback(
    () => listDescriptionVersions({ status: 'pending', limit: 50 }),
    [],
  )
  const fetchHistory = useCallback(
    () => listDescriptionVersions({
      status: statusFilter || undefined,
      limit: 100,
    }),
    [statusFilter],
  )

  const { data: pendingData, refetch: refetchPending } = useApi(fetchPending)
  const { data: historyData, refetch: refetchHistory } = useApi(fetchHistory)

  const pending = pendingData?.data ?? []
  const history = historyData?.data ?? []

  function handleResolved() {
    refetchPending()
    refetchHistory()
  }

  return (
    <div className="space-y-6">
      <p className="text-sm text-muted-foreground">
        Review AI-suggested description improvements and override the descriptions shown to MCP
        clients for built-in tools.
      </p>

      {pending.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-sm font-medium text-muted-foreground">
            Pending Review ({pending.length})
          </h2>
          {pending.map((v) => (
            <PendingCard key={v.id} version={v} onResolved={handleResolved} />
          ))}
        </div>
      )}

      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between">
            <CardTitle className="text-base">Version History</CardTitle>
            <div className="flex gap-1.5">
              {['', 'active', 'pending', 'superseded', 'rejected'].map((s) => (
                <Button
                  key={s}
                  size="sm"
                  variant={statusFilter === s ? 'default' : 'outline'}
                  className="h-7 text-xs"
                  data-testid={`description-filter-${s || 'all'}`}
                  onClick={() => setStatusFilter(s)}
                >
                  {s || 'All'}
                </Button>
              ))}
            </div>
          </div>
        </CardHeader>
        <CardContent className="pt-0">
          <HistoryTable versions={history} onRollback={handleResolved} />
        </CardContent>
      </Card>

      <BuiltinDescriptionOverrides />
    </div>
  )
}
