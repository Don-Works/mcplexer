import type { Dispatch, SetStateAction } from 'react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { AuthScope, DownstreamServer, Workspace } from '@/api/types'
import { RouteRuleFormFields } from '@/components/routes/RouteRuleFormFields'
import {
  emptyRouteForm,
  type RouteFormData,
} from '@/components/routes/route-form-model'

export type { RouteFormData } from '@/components/routes/route-form-model'
export const emptyForm = emptyRouteForm

interface RouteDialogProps {
  open: boolean
  onClose: () => void
  form: RouteFormData
  setForm: Dispatch<SetStateAction<RouteFormData>>
  onSave: () => void
  onDelete?: () => void
  saving: boolean
  editing: boolean
  saveError: string | null
  workspaces: Pick<Workspace, 'id' | 'name'>[]
  downstreams: Pick<DownstreamServer, 'id' | 'name' | 'tool_namespace'>[]
  authScopes: AuthScope[]
}

export function RouteDialog({
  open,
  onClose,
  form,
  setForm,
  onSave,
  onDelete,
  saving,
  editing,
  workspaces,
  downstreams,
  authScopes,
  saveError,
}: RouteDialogProps) {
  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? 'Edit Access Rule' : 'Add Access Rule'}</DialogTitle>
          <DialogDescription>
            An access rule connects a server to a workspace. When an agent's
            tool call matches, this rule decides which server handles it, which
            credentials to use, and whether approval is required.
          </DialogDescription>
        </DialogHeader>

        <RouteRuleFormFields
          form={form}
          setForm={setForm}
          visible={open}
          resetKey={editing ? 'edit' : 'new'}
          workspaces={workspaces}
          downstreams={downstreams}
          authScopes={authScopes}
          saveError={saveError}
        />

        <DialogFooter className="gap-2 sm:justify-between">
          <div>
            {editing && onDelete && (
              <Button
                variant="destructive"
                onClick={onDelete}
                data-testid="route-delete-from-dialog"
              >
                Remove route
              </Button>
            )}
          </div>
          <div className="flex flex-col-reverse gap-2 sm:flex-row">
            <Button variant="outline" onClick={onClose} data-testid="route-cancel">
              Cancel
            </Button>
            <Button
              onClick={onSave}
              data-testid="route-save"
              disabled={
                saving ||
                !form.workspace_id ||
                (form.policy === 'allow' && !form.downstream_server_id)
              }
            >
              {saving ? 'Saving...' : 'Save'}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
