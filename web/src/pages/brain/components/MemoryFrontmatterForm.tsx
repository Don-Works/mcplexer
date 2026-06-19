import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { TagTokenInput } from './TagTokenInput'
import { EntityInput } from './EntityInput'
import { GhostTextarea } from './GhostText'
import type { GhostState } from './useGhostText'
import {
  Field,
  ReadOnlyMeta,
  PinnedToggle,
  errFor,
  rfc3339ToLocalInput,
  localInputToRfc3339,
  type FieldError,
} from './frontmatterControls'
import type { BrainMemoryRecord } from '@/api/brainBrowser'

// MemoryFrontmatterForm renders a memory record document-first (DESIGN §3.2,
// re-shaped for the "warm room" brain): the title and the writing area lead,
// the way a note reads in any familiar notes tool. Everything structured
// (tags, links, validity, pin) is demoted to a quiet Details section below the
// prose, and the machine provenance (id, source, timestamps) sits last. A note
// is a free-form note; a fact is auto-recalled and can carry a "valid from"
// date. The toggle between the two stays available but unobtrusive. All AI
// assist (ghost-text, candidate rail, guidance) keys off the same content/title
// it always did, so the assist machinery is untouched.
export function MemoryFrontmatterForm({
  rec,
  err,
  onChange,
  onGhostState,
}: {
  rec: BrainMemoryRecord
  err: FieldError | null
  onChange: (r: BrainMemoryRecord) => void
  onGhostState?: (s: GhostState) => void
}) {
  const isFact = (rec.kind || 'note') === 'fact'
  return (
    <div className="space-y-4">
      {/* Title — the note's name (also its unique key). Kept large and plain;
          no "unique key" jargon in the label. */}
      <Field label={isFact ? 'Fact name' : 'Title'} error={errFor(err, 'name')}>
        {({ controlId }) => (
          <Input
            id={controlId}
            className="h-auto border-0 border-b border-border bg-transparent px-0 py-1 text-xl font-semibold focus-visible:ring-0 focus-visible:border-primary"
            placeholder={isFact ? 'Name this fact' : 'Untitled note'}
            value={rec.name}
            onChange={(e) => onChange({ ...rec, name: e.target.value })}
          />
        )}
      </Field>

      {/* The writing area — the hero of the note, directly under the title. */}
      <Field label={isFact ? 'What is true' : 'Write'}>
        {({ controlId }) => (
          <GhostTextarea
            id={controlId}
            value={rec.content}
            onChange={(content) => onChange({ ...rec, content })}
            field="content"
            workspace={rec.workspace}
            onState={onGhostState}
          />
        )}
      </Field>

      {/* Details — everything structured, tucked beneath the prose so the
          note reads like a document, not a form. */}
      <div className="space-y-3 border-t border-border pt-3">
        <p className="text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/60">
          Details
        </p>

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

        <Field label="Linked to">
          {({ labelId }) => (
            <EntityInput
              entities={rec.entities ?? []}
              onChange={(entities) => onChange({ ...rec, entities })}
              ariaLabelledBy={labelId}
            />
          )}
        </Field>

        <Field label="Type">
          {({ labelId }) => (
            <ToggleGroup
              type="single"
              value={rec.kind || 'note'}
              onValueChange={(v) => v && onChange({ ...rec, kind: v })}
              variant="outline"
              aria-labelledby={labelId}
            >
              <ToggleGroupItem value="note" className="rounded-none text-xs">
                Note
              </ToggleGroupItem>
              <ToggleGroupItem value="fact" className="rounded-none text-xs">
                Fact
              </ToggleGroupItem>
            </ToggleGroup>
          )}
        </Field>

        {isFact && (
          <Field label="Valid from">
            {({ controlId }) => (
              <div className="space-y-1.5">
                <Input
                  id={controlId}
                  type="datetime-local"
                  disabled={!rec.t_valid_start}
                  value={rfc3339ToLocalInput(rec.t_valid_start)}
                  onChange={(e) => onChange({ ...rec, t_valid_start: localInputToRfc3339(e.target.value) })}
                />
                <label className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Checkbox
                    checked={!rec.t_valid_start}
                    onCheckedChange={(v) =>
                      onChange({ ...rec, t_valid_start: v ? undefined : new Date().toISOString() })
                    }
                  />
                  Currently valid (valid from now)
                </label>
              </div>
            )}
          </Field>
        )}

        <PinnedToggle pinned={rec.pinned} onChange={(pinned) => onChange({ ...rec, pinned })} />
      </div>

      <ReadOnlyMeta id={rec.id} source={rec.source} created={rec.created_at} updated={rec.updated_at} />
    </div>
  )
}
