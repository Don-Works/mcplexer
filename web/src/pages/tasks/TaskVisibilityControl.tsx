import { useCallback, useEffect, useMemo, useState } from 'react'
import { Eye, Loader2, LockKeyhole, UsersRound } from 'lucide-react'
import { toast } from 'sonner'

import {
  getCollaboration,
  getTaskAccess,
  setTaskVisibility,
  type TaskVisibility,
} from '@/api/collaboration'
import type { Task } from '@/api/tasks'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useApi } from '@/hooks/use-api'

const visibilityCopy: Record<TaskVisibility, { label: string; detail: string }> = {
  private: { label: 'Private', detail: 'Only the task owner and local workspace owner.' },
  restricted: { label: 'Named people', detail: 'Only selected principals who also hold workspace read access.' },
  workspace: { label: 'Workspace', detail: 'Every principal with workspace view and task read access.' },
}

export function TaskVisibilityControl({ task, onUpdate }: { task: Task; onUpdate: () => void }) {
  const fetcher = useCallback((signal: AbortSignal) => getCollaboration(signal), [])
  const { data } = useApi(fetcher)
  const accessFetcher = useCallback((signal: AbortSignal) => getTaskAccess(task.id, signal), [task.id])
  const { data: access, refetch: refetchAccess } = useApi(accessFetcher)
  const [open, setOpen] = useState(false)
  const [visibility, setVisibility] = useState<TaskVisibility>(task.visibility ?? 'private')
  const [audience, setAudience] = useState<string[]>(task.audience_principal_ids ?? [])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setVisibility(access?.visibility ?? task.visibility ?? 'private')
    setAudience(access?.audience_principal_ids ?? [])
  }, [access, task.visibility, task.visibility_epoch])

  const workspace = data?.workspaces.find((row) => row.local_workspace_id === task.workspace_id)
  const eligible = useMemo(() => {
    if (!data || !workspace) return []
    return data.principals.filter((principal) =>
      !principal.is_local_owner && principal.status === 'active' &&
      workspace.grants.some((grant) => grant.principal_id === principal.id && !grant.revoked_at && grant.capability === 'workspace.view') &&
      workspace.grants.some((grant) => grant.principal_id === principal.id && !grant.revoked_at && grant.capability === 'tasks.read'),
    )
  }, [data, workspace])

  const save = async () => {
    setBusy(true)
    try {
      await setTaskVisibility(task.id, visibility, visibility === 'restricted' ? audience : [])
      toast.success(`Task visibility set to ${visibilityCopy[visibility].label.toLowerCase()}`)
      setOpen(false)
      refetchAccess()
      onUpdate()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Could not change visibility')
    } finally {
      setBusy(false)
    }
  }

  const current = (access?.visibility ?? task.visibility ?? 'private') as TaskVisibility
  const currentEpoch = access?.visibility_epoch ?? task.visibility_epoch ?? 1
  const editable = access?.visibility_editable === true
  return (
    <section>
      <div className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">Sharing</div>
      <div className="border border-border bg-card/40 p-3">
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-start gap-2">
            {current === 'private' ? <LockKeyhole className="mt-0.5 h-4 w-4 text-muted-foreground" /> : current === 'restricted' ? <UsersRound className="mt-0.5 h-4 w-4 text-sky-300" /> : <Eye className="mt-0.5 h-4 w-4 text-emerald-300" />}
            <div>
              <div className="flex items-center gap-2 text-xs font-medium">
                {visibilityCopy[current].label}
                <Badge variant="outline" tone="mono" className="text-[8px]">epoch {currentEpoch}</Badge>
              </div>
              <p className="mt-1 text-[10px] leading-4 text-muted-foreground">{visibilityCopy[current].detail}</p>
            </div>
          </div>
          <Button size="sm" variant="ghost" className="h-7 text-[10px]" onClick={() => setOpen(true)} disabled={!editable} title={editable ? 'Change task visibility' : 'Visibility is controlled by the workspace home'}>{editable ? 'Change' : 'Home controlled'}</Button>
        </div>
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Who can read this task?</DialogTitle>
            <DialogDescription>
              Workspace permissions are necessary but not sufficient. This task-level choice is checked on every sync and payload request.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            {(Object.keys(visibilityCopy) as TaskVisibility[]).map((value) => (
              <button
                key={value}
                type="button"
                onClick={() => setVisibility(value)}
                className={`w-full border p-3 text-left transition-colors ${visibility === value ? 'border-primary bg-primary/10' : 'border-border hover:bg-muted/30'}`}
              >
                <div className="text-sm font-medium">{visibilityCopy[value].label}</div>
                <div className="mt-1 text-xs text-muted-foreground">{visibilityCopy[value].detail}</div>
              </button>
            ))}
            {visibility === 'restricted' ? (
              <div className="border border-border p-3">
                <div className="mb-2 text-xs font-medium">Named audience</div>
                <div className="space-y-2">
                  {eligible.map((principal) => (
                    <label key={principal.id} className="flex cursor-pointer items-center gap-3 text-xs">
                      <Checkbox
                        checked={audience.includes(principal.id)}
                        onCheckedChange={(checked) => setAudience((currentAudience) =>
                          checked === true
                            ? [...currentAudience, principal.id]
                            : currentAudience.filter((id) => id !== principal.id),
                        )}
                      />
                      <span>{principal.display_name}</span>
                      <span className="text-[10px] text-muted-foreground">{principal.kind}</span>
                    </label>
                  ))}
                  {eligible.length === 0 ? <p className="text-xs text-muted-foreground">Grant someone Reader access to this workspace first.</p> : null}
                </div>
              </div>
            ) : null}
            {workspace?.policy ? (
              <p className="text-[11px] leading-5 text-muted-foreground">
                Agent ceiling: <span className="font-mono text-foreground">{workspace.policy.agent_visibility_ceiling}</span>. Existing tasks require approval to widen: <span className="font-mono text-foreground">{workspace.policy.widening_requires_approval ? 'yes' : 'no'}</span>.
              </p>
            ) : null}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>Cancel</Button>
            <Button onClick={save} disabled={busy || (visibility === 'restricted' && audience.length === 0)}>
              {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              Save visibility
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}
