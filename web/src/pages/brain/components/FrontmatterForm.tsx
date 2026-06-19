import { useMemo } from 'react'
import { Input } from '@/components/ui/input'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { TagTokenInput } from './TagTokenInput'
import { RefTokenInput } from './RefTokenInput'
import { GhostTextarea } from './GhostText'
import type { GhostState } from './useGhostText'
import {
  Field,
  ReadOnlyMeta,
  PinnedToggle,
  AssigneeSelect,
  errFor,
  rfc3339ToDateInput,
  dateInputToRfc3339,
  type FieldError,
} from './frontmatterControls'
import type { BrainTaskRecord } from '@/api/brainBrowser'

// FrontmatterForm is the field-mapping engine (DESIGN §3.2): every frontmatter
// field becomes a friendly control, the user never types YAML, and read-only
// machine-truth fields (id / source / timestamps) are shown in mono — not
// hidden. status/priority are ToggleGroups so off-vocab is unselectable; the
// offending 422 field renders its error inline at the control via Field. The
// memory variant lives in MemoryFrontmatterForm; shared sub-controls + the
// FieldError type live in frontmatterControls.
export { Field, type FieldError } from './frontmatterControls'
export { MemoryFrontmatterForm } from './MemoryFrontmatterForm'

const PRIORITY_OPTS = ['', 'low', 'med', 'high']

// TaskFrontmatterForm renders the task/v1 field set (DESIGN §3.2 table).
export function TaskFrontmatterForm({
  rec,
  vocab,
  err,
  onChange,
  onGhostState,
}: {
  rec: BrainTaskRecord
  vocab: string[]
  err: FieldError | null
  onChange: (r: BrainTaskRecord) => void
  // onGhostState surfaces the body ghost-text state so the editor can drive a
  // single shared ModelPresenceLabel (DESIGN §4.2). Optional — omit to skip.
  onGhostState?: (s: GhostState) => void
}) {
  // Status options come from the workspace vocab (off-vocab unselectable). The
  // current value is folded in so the control still shows an off-vocab value.
  const statusOpts = useMemo(() => {
    const set = new Set(vocab)
    if (rec.status) set.add(rec.status)
    return Array.from(set)
  }, [vocab, rec.status])
  const statusOffVocab = rec.status !== '' && vocab.length > 0 && !vocab.includes(rec.status)

  return (
    <div className="space-y-3">
      <ReadOnlyMeta id={rec.id} source={rec.source} created={rec.created_at} updated={rec.updated_at} />

      <Field label="Title">
        {({ controlId }) => (
          <Input
            id={controlId}
            value={rec.title}
            onChange={(e) => onChange({ ...rec, title: e.target.value })}
          />
        )}
      </Field>

      <Field label="Status" error={errFor(err, 'status')}>
        {({ controlId, labelId }) =>
          statusOpts.length > 0 ? (
            <>
              <ToggleGroup
                type="single"
                value={rec.status}
                onValueChange={(v) => v && onChange({ ...rec, status: v })}
                variant="outline"
                aria-labelledby={labelId}
              >
                {statusOpts.map((s) => (
                  <ToggleGroupItem key={s} value={s} className="rounded-none font-mono text-xs">
                    {s}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
              {statusOffVocab && <p className="text-xs text-amber-300">current value off-vocab</p>}
            </>
          ) : (
            <>
              <Input
                id={controlId}
                value={rec.status}
                onChange={(e) => onChange({ ...rec, status: e.target.value })}
                placeholder="open / doing / review / done"
              />
              {statusOffVocab && <p className="text-xs text-amber-300">current value off-vocab</p>}
            </>
          )
        }
      </Field>

      <Field label="Priority">
        {({ labelId }) => (
          <ToggleGroup
            type="single"
            value={rec.priority ?? ''}
            onValueChange={(v) => onChange({ ...rec, priority: v })}
            variant="outline"
            aria-labelledby={labelId}
          >
            {PRIORITY_OPTS.map((p) => (
              <ToggleGroupItem key={p || 'none'} value={p} className="rounded-none font-mono text-xs">
                {p || 'none'}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        )}
      </Field>

      <div className="grid grid-cols-2 gap-3">
        <Field label="Due date">
          {({ controlId }) => (
            <Input
              id={controlId}
              type="date"
              value={rfc3339ToDateInput(rec.due_at)}
              onChange={(e) => onChange({ ...rec, due_at: dateInputToRfc3339(e.target.value) })}
            />
          )}
        </Field>
        <Field label="Assignee">
          {({ labelId }) => (
            <AssigneeSelect
              value={rec.assignee}
              onChange={(a) => onChange({ ...rec, assignee: a })}
              ariaLabelledBy={labelId}
            />
          )}
        </Field>
      </div>

      <Field label="Tags">
        {({ labelId }) => (
          <TagTokenInput
            tags={rec.tags ?? []}
            onChange={(tags) => onChange({ ...rec, tags })}
            workspace={rec.workspace}
            ariaLabelledBy={labelId}
          />
        )}
      </Field>

      <Field label="Composes (child records)">
        {({ labelId }) => (
          <RefTokenInput
            refs={rec.composes ?? []}
            onChange={(composes) => onChange({ ...rec, composes })}
            workspace={rec.workspace}
            ariaLabelledBy={labelId}
          />
        )}
      </Field>

      <PinnedToggle pinned={rec.pinned} onChange={(pinned) => onChange({ ...rec, pinned })} />

      <Field label="Description (Markdown)">
        {({ controlId }) => (
          <GhostTextarea
            id={controlId}
            value={rec.description}
            onChange={(description) => onChange({ ...rec, description })}
            field="description"
            workspace={rec.workspace}
            onState={onGhostState}
          />
        )}
      </Field>
    </div>
  )
}
