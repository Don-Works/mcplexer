import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { bulkCreateRoutes } from '@/api/client'
import type { DownstreamServer, Workspace } from '@/api/types'
import { toast } from 'sonner'

interface BulkEnableDialogProps {
  open: boolean
  onClose: () => void
  workspace: Workspace
  downstreams: DownstreamServer[]
  enabledDownstreamIds: Set<string>
  onSuccess: () => void
}

export function BulkEnableDialog({
  open,
  onClose,
  workspace,
  downstreams,
  enabledDownstreamIds,
  onSuccess,
}: BulkEnableDialogProps) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [saving, setSaving] = useState(false)
  const [showDefaults, setShowDefaults] = useState(false)
  const [priority, setPriority] = useState(100)
  const [approvalMode, setApprovalMode] = useState<'none' | 'write' | 'all'>('none')

  const available = downstreams.filter((d) => !d.disabled)

  function toggle(id: string) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  async function handleSave() {
    if (selected.size === 0) return
    setSaving(true)
    try {
      const rules = Array.from(selected).map((dsId) => {
        const ds = downstreams.find((d) => d.id === dsId)
        return {
          name: `${workspace.name} → ${ds?.name ?? dsId}`,
          priority,
          workspace_id: workspace.id,
          path_glob: '**',
          tool_match: [],
          scope_policy: {},
          downstream_server_id: dsId,
          auth_scope_id: '',
          policy: 'allow' as const,
          log_level: 'info',
          approval_mode: approvalMode,
          approval_timeout: 300,
        }
      })
      await bulkCreateRoutes(rules)
      toast.success(`Enabled ${rules.length} server${rules.length > 1 ? 's' : ''}`)
      setSelected(new Set())
      onSuccess()
      onClose()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to create routes')
    } finally {
      setSaving(false)
    }
  }

  useEffect(() => {
    if (!open) return
    setSelected(new Set())
    setPriority(100)
    setApprovalMode('none')
    setShowDefaults(false)
  }, [open])

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Enable Servers for {workspace.name}</DialogTitle>
        </DialogHeader>
        <div className="space-y-3 max-h-[50vh] overflow-y-auto pr-1">
          {available.length === 0 ? (
            <p className="text-sm text-muted-foreground">No downstream servers available.</p>
          ) : (
            available.map((ds) => {
              const alreadyEnabled = enabledDownstreamIds.has(ds.id)
              return (
                <label
                  key={ds.id}
                  className="flex items-center gap-3 rounded-md border border-border/50 p-3 cursor-pointer hover:bg-muted/30 transition-colors"
                >
                  <Checkbox
                    checked={alreadyEnabled || selected.has(ds.id)}
                    disabled={alreadyEnabled}
                    onCheckedChange={() => toggle(ds.id)}
                  />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium truncate">{ds.name}</div>
                    <div className="text-xs text-muted-foreground truncate">{ds.tool_namespace}</div>
                  </div>
                  {alreadyEnabled && (
                    <Badge variant="outline" className="text-xs shrink-0">
                      already enabled
                    </Badge>
                  )}
                </label>
              )
            })
          )}
        </div>

        <button
          type="button"
          className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
          onClick={() => setShowDefaults(!showDefaults)}
        >
          {showDefaults ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
          Defaults
        </button>

        {showDefaults && (
          <div className="space-y-3 rounded-md border border-border/50 p-3">
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Priority</Label>
              <Input
                type="number"
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value))}
                className="w-32"
              />
            </div>
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">Approval</Label>
              <Select
                value={approvalMode}
                onValueChange={(v) => setApprovalMode(v as 'none' | 'write' | 'all')}
              >
                <SelectTrigger className="w-48">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">None</SelectItem>
                  <SelectItem value="write">Write only (destructive)</SelectItem>
                  <SelectItem value="all">All tool calls</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={saving || selected.size === 0}>
            {saving ? 'Saving...' : `Enable ${selected.size} server${selected.size !== 1 ? 's' : ''}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
