import { useId } from 'react'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { CopyButton } from '@/components/ui/copy-button'
import type { BrainAssignee } from '@/api/brainBrowser'

// frontmatterControls holds the shared sub-controls + datetime helpers the
// task and memory FrontmatterForms both use (DESIGN §3.2). Splitting them out
// keeps each form file under the 300-line cap without duplicating the Field
// wrapper, the read-only machine-truth meta, or the assignee picker.

// FieldError is the structured 422 (or standing validation) error scoped to a
// single control: the human-readable message + the allowed-vocab hint.
export interface FieldError {
  field?: string
  message: string
  allowed?: string[]
}

// errFor returns the FieldError when it targets `field`, else undefined — so a
// control renders its own inline error and no other.
export function errFor(err: FieldError | null | undefined, field: string): FieldError | undefined {
  return err && err.field === field ? err : undefined
}

// --- datetime helpers (RFC3339 <-> input value) -----------------------------
export function rfc3339ToDateInput(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString().slice(0, 10)
}
export function dateInputToRfc3339(v: string): string | undefined {
  if (!v) return undefined
  const d = new Date(`${v}T00:00:00Z`)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}
export function rfc3339ToLocalInput(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
export function localInputToRfc3339(v: string): string | undefined {
  if (!v) return undefined
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}
function shortIso(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString().slice(0, 16).replace('T', ' ')
}

// AssigneeSelect maps assignee.origin_kind + session_id onto a Select. The
// options are unassigned / local (this session) / keeping the existing value;
// the session id is shown in mono. The full active-session picker lands with
// the shared mesh/session index; this is the structural control.
export function AssigneeSelect({
  value,
  onChange,
  ariaLabelledBy,
}: {
  value?: BrainAssignee
  onChange: (a: BrainAssignee | undefined) => void
  ariaLabelledBy?: string
}) {
  const current = value?.session_id
    ? `kept:${value.session_id}`
    : value?.origin_kind
      ? 'local'
      : 'unassigned'
  return (
    <Select
      value={current}
      onValueChange={(v) => {
        if (v === 'unassigned') onChange(undefined)
        else if (v === 'local') onChange({ origin_kind: 'local' })
        // "kept:*" keeps the loaded assignee untouched.
      }}
    >
      <SelectTrigger className="rounded-none" aria-labelledby={ariaLabelledBy}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="unassigned">unassigned</SelectItem>
        <SelectItem value="local">local (this session)</SelectItem>
        {value?.session_id && (
          <SelectItem value={`kept:${value.session_id}`}>
            <span className="font-mono text-xs">
              {value.origin_kind ?? 'agent'} · {value.session_id}
            </span>
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  )
}

// ReadOnlyMeta shows the machine-truth fields the user never edits: the mono
// id (+ copy), the source provenance chip, and the created/updated micro
// timestamps. Never hidden (DESIGN §3.2 — read-only fields are mono truth).
export function ReadOnlyMeta({
  id,
  source,
  created,
  updated,
}: {
  id?: string
  source?: { kind?: string; session_id?: string }
  created?: string
  updated?: string
}) {
  if (!id && !source?.kind && !created && !updated) return null
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border pb-2 text-[10px] text-muted-foreground">
      {id && (
        <span className="flex items-center gap-1">
          <span className="font-mono">{id}</span>
          <CopyButton value={id} />
        </span>
      )}
      {source?.kind && (
        <span>
          source{' '}
          <span className="font-mono">
            {source.kind}
            {source.session_id ? ` · ${source.session_id}` : ''}
          </span>
        </span>
      )}
      {created && <span className="font-mono">created {shortIso(created)}</span>}
      {updated && <span className="font-mono">updated {shortIso(updated)}</span>}
    </div>
  )
}

export function PinnedToggle({ pinned, onChange }: { pinned: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center gap-2 text-sm">
      <Checkbox checked={pinned} onCheckedChange={(v) => onChange(Boolean(v))} />
      Pinned
    </label>
  )
}

// FieldA11y carries the generated ids a Field hands to its control so the
// label is announced by a screen reader when the control gets focus:
//   - controlId: set as `id` on a single native control (Input/textarea) so the
//     <Label htmlFor> association resolves.
//   - labelId: set as `aria-labelledby` on COMPOSITE controls (ToggleGroup,
//     Select, token inputs, GhostTextarea) whose focusable root is not a single
//     labelable element — htmlFor cannot reach those, aria-labelledby can.
export interface FieldA11y {
  controlId: string
  labelId: string
}

// Field wraps a labelled control + an inline 422 error (with the allowed-vocab
// hint) directly beneath the offending control (DESIGN §3.7 + §7 a11y). The
// label is associated with the control via generated ids: children is a
// render-prop so each control can claim controlId (native) or labelId
// (composite). A plain-node children form is still accepted for label-less
// decorative content, but callers SHOULD use the render-prop so the label is
// programmatically announced.
export function Field({
  label,
  error,
  children,
}: {
  label: string
  error?: FieldError
  children: React.ReactNode | ((ids: FieldA11y) => React.ReactNode)
}) {
  const controlId = useId()
  const labelId = useId()
  // A render-prop child claims controlId on a native control; otherwise the
  // label still points its htmlFor at controlId (harmless when unmatched) and
  // exposes labelId for any composite root to reference via aria-labelledby.
  return (
    <div className="space-y-1.5">
      <Label id={labelId} htmlFor={controlId} className="text-xs text-muted-foreground">
        {label}
      </Label>
      {typeof children === 'function' ? children({ controlId, labelId }) : children}
      {error && (
        <p className="text-xs text-red-300">
          {error.message}
          {error.allowed && error.allowed.length > 0 && (
            <span className="text-muted-foreground"> allowed: {error.allowed.join(' · ')}</span>
          )}
        </p>
      )}
    </div>
  )
}
